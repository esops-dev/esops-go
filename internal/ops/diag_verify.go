package ops

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/dj-wasabi/esops-go/internal/types"
)

// Decompression-bomb caps for VerifyDiagBundle. The reader is gzipped
// + tar'd, so io.Copy without an upper bound is a CWE-409 hazard: a
// malicious bundle could expand into terabytes and exhaust memory on
// the verifier (a support engineer running `verify` against a bundle
// from an untrusted reporter is the realistic threat). Caps are deep
// above any legitimate bundle:
//
//   - maxBundleEntryBytes (5 GiB): the largest legitimate single
//     artifact ever observed in production is /_all/_mapping on a
//     ~100k-index cluster, well under 1 GiB. 5 GiB leaves room.
//   - maxManifestBytes (64 MiB): the manifest is a JSON index, never
//     more than a few MiB even on huge bundles. 64 MiB is paranoid.
//
// If a real bundle ever hits these limits, lift them via flag — but
// "infinity" should never be the contract.
const (
	maxBundleEntryBytes = 5 << 30
	maxManifestBytes    = 64 << 20
)

// VerifyRequest is the input to VerifyDiagBundle. Reader carries the
// gzipped tarball; ExpectSHA256, when non-empty, is the manifest hash
// the operator recorded when the bundle was created — verifying it
// closes the loop on tamper-evidence (a modified manifest yields a
// different hash; a modified artifact yields a different per-artifact
// hash that mismatches the manifest's recorded value).
type VerifyRequest struct {
	Reader       io.Reader
	ExpectSHA256 string
}

// copyLimited copies from src to dst with a hard cap of `max` bytes.
// Wraps src in io.LimitReader(max+1) so a payload exactly at the cap
// reads cleanly and one over trips the post-copy size check. Used in
// the verify path where src is a gzipped tar entry — gosec's G110
// rule requires an explicit upper bound on every io.Copy from a
// compressed source, and "infinity" is never the right answer for a
// bundle the user may not have produced.
func copyLimited(dst io.Writer, src io.Reader, max int64, label string) error {
	n, err := io.Copy(dst, io.LimitReader(src, max+1))
	if err != nil {
		return err
	}
	if n > max {
		return fmt.Errorf("%s exceeded size cap of %d bytes (decompression-bomb guard)", label, max)
	}
	return nil
}

// readAllLimited buffers up to `max` bytes from src and refuses bigger
// payloads. Equivalent of io.ReadAll for the manifest path, where we
// genuinely need the whole body in memory but still want a bound to
// silence gosec G110 and protect against a 50 GB "manifest.json".
func readAllLimited(src io.Reader, max int64, label string) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(src, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > max {
		return nil, fmt.Errorf("%s exceeded size cap of %d bytes (decompression-bomb guard)", label, max)
	}
	return buf, nil
}

// VerifyDiagBundle reads a gzipped diag tarball and checks every
// artifact's SHA-256 against the manifest's recorded value. The bundle
// streams through once: each non-manifest entry is hashed on the fly
// (only the running hash state is held in memory, not the body), the
// manifest itself is small and buffered in full. Holding hashes for
// every artifact + the manifest bytes is the entire memory footprint,
// independent of bundle size — verifying a 2 GB --include-mappings
// bundle uses the same memory as a 50 MB one.
//
// Returns a structured result for renderers; never returns an error
// for content-level failures (mismatch, missing, unknown entry) —
// those land in the result's Summary and the Pass flag. Errors are
// reserved for I/O problems, malformed gzip/tar, missing manifest, or
// an explicit ExpectSHA256 that didn't match (because that's the
// operator's own assertion failing — distinct from the bundle's
// internal consistency).
func VerifyDiagBundle(ctx context.Context, req VerifyRequest) (types.DiagVerifyResult, error) {
	gz, err := gzip.NewReader(req.Reader)
	if err != nil {
		return types.DiagVerifyResult{}, fmt.Errorf("opening gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	computed := map[string]string{}
	var manifestBytes []byte

	for {
		if ctx.Err() != nil {
			return types.DiagVerifyResult{}, ctx.Err()
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return types.DiagVerifyResult{}, fmt.Errorf("reading tar: %w", err)
		}
		// Pre-screen: a header declaring more bytes than our cap is a
		// fast fail with a useful message, before we waste cycles
		// streaming through it.
		if hdr.Size > maxBundleEntryBytes {
			return types.DiagVerifyResult{}, fmt.Errorf(
				"entry %q declares %d bytes, exceeds cap %d (decompression-bomb guard)",
				hdr.Name, hdr.Size, maxBundleEntryBytes)
		}
		// Only regular files are interesting; tar headers can carry
		// directories or PAX-extension entries we shouldn't hash.
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA { //nolint:staticcheck // TypeRegA is deprecated but still appears in some bundles
			if err := copyLimited(io.Discard, tr, maxBundleEntryBytes, hdr.Name); err != nil {
				return types.DiagVerifyResult{}, fmt.Errorf("skipping %q: %w", hdr.Name, err)
			}
			continue
		}
		if hdr.Name == "manifest.json" {
			buf, err := readAllLimited(tr, maxManifestBytes, "manifest.json")
			if err != nil {
				return types.DiagVerifyResult{}, fmt.Errorf("reading manifest: %w", err)
			}
			manifestBytes = buf
			continue
		}
		h := sha256.New()
		if err := copyLimited(h, tr, maxBundleEntryBytes, hdr.Name); err != nil {
			return types.DiagVerifyResult{}, fmt.Errorf("hashing %q: %w", hdr.Name, err)
		}
		computed[hdr.Name] = hex.EncodeToString(h.Sum(nil))
	}

	if manifestBytes == nil {
		return types.DiagVerifyResult{}, errors.New("manifest.json is missing from the bundle")
	}

	manifestSum := sha256.Sum256(manifestBytes)
	manifestHex := hex.EncodeToString(manifestSum[:])

	if req.ExpectSHA256 != "" && req.ExpectSHA256 != manifestHex {
		return types.DiagVerifyResult{ManifestSHA256: manifestHex},
			fmt.Errorf("manifest sha256 mismatch: expected %s, got %s", req.ExpectSHA256, manifestHex)
	}

	var manifest types.DiagManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return types.DiagVerifyResult{ManifestSHA256: manifestHex},
			fmt.Errorf("parsing manifest: %w", err)
	}

	result := types.DiagVerifyResult{
		ManifestSHA256: manifestHex,
		Cluster:        manifest.Cluster,
		Tool:           manifest.Tool,
		Version:        manifest.Version,
		CreatedAt:      manifest.CreatedAt,
	}

	listed := map[string]bool{}
	for _, a := range manifest.Artifacts {
		listed[a.File] = true
		actual := computed[a.File]
		v := types.DiagVerifyArtifact{
			Name:     a.Name,
			File:     a.File,
			Expected: a.SHA256,
			Actual:   actual,
		}
		switch {
		case a.SHA256 == "" && actual == "":
			// Manifest didn't record a hash AND no tar entry exists —
			// this is the transport-failure case (collector never got
			// a body). Counts as no-hash; no expectation to verify.
			v.Status = types.DiagVerifyNoHash
			result.Summary.NoHash++
		case a.SHA256 == "":
			// No recorded hash, but a tar entry exists. We can't
			// verify integrity without the recorded value. Surface
			// the actual hash so the operator can decide whether to
			// trust it; status stays no_hash.
			v.Status = types.DiagVerifyNoHash
			result.Summary.NoHash++
		case actual == "":
			v.Status = types.DiagVerifyMissing
			result.Summary.Missing++
		case actual == a.SHA256:
			v.Status = types.DiagVerifyOK
			result.Summary.OK++
		default:
			v.Status = types.DiagVerifyMismatch
			result.Summary.Mismatched++
		}
		result.Summary.Total++
		result.Artifacts = append(result.Artifacts, v)
	}

	// Surface tar entries not listed in the manifest. Could be benign
	// (a future format extension) or suspicious (someone bolted on an
	// extra file post-hoc); either way the operator wants to see them.
	for name := range computed {
		if !listed[name] {
			result.UnknownEntries = append(result.UnknownEntries, name)
			result.Summary.Unknown++
		}
	}
	sort.Strings(result.UnknownEntries)

	result.Pass = result.Summary.Mismatched == 0 &&
		result.Summary.Missing == 0 &&
		result.Summary.Unknown == 0
	return result, nil
}
