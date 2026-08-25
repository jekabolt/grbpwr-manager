package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
)

// ------------------------------------------------------------------------------------------
// THE BODY CEILING OF THE IMPORT ROUTE, GUARDED AT THE ROUTE.
//
// The archive import handler already has a test for what it does when a body runs over the
// ceiling (internal/apisrv/admin, TestTechcardArchiveUploadRefusesABodyOverTheCeiling): 413 and
// not 500. That test cannot reach setupHTTPAPI — limitBody is unexported here and the handler
// lives in another package — so it stands up a MaxBytesReader of its own. Which means it is a
// guard standing next to a COPY: delete `limitBody(maxImportArchiveBodyBytes)` from the route
// below and that test stays green, because it never touched the route.
//
// This file is the guard at the route itself. It raises the PRODUCTION router — the same
// setupHTTPAPI the process runs, with every wrapper it puts around /api — and pushes a body one
// byte over the ceiling at the real path, then asks the handler what it was actually handed.
// Removing the wrap from the route turns it red; that is the whole point of it existing.
//
// It also pins the NUMBER and not merely the presence of a cap: http.MaxBytesError carries the
// limit it was built with, so a cap silently retyped to the files-library 95 MiB fails here too.
// ------------------------------------------------------------------------------------------

// tcapBody is a request body of exactly n bytes that costs nothing to produce: it hands back
// length without touching the buffer, because the bytes are discarded and only their COUNT is
// under test. A bytes.Reader of 256 MiB would allocate a quarter of a gigabyte to prove the same
// thing.
type tcapBody struct{ left int64 }

func (b *tcapBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > b.left {
		n = b.left
	}
	b.left -= n
	return int(n), nil
}

func TestImportArchiveRouteCarriesTheFormatBodyCeiling(t *testing.T) {
	// The cap is READ FROM THE FORMAT, and that is the first half of the claim: two numbers for
	// one question is how "is this archive too big" acquires two answers (the bucket's capReader
	// enforces the same constant a second time).
	if maxImportArchiveBodyBytes != techcardarchive.MaxUploadedArchiveBytes {
		t.Fatalf("the route's cap must BE the format's number, got %d vs %d",
			maxImportArchiveBodyBytes, techcardarchive.MaxUploadedArchiveBytes)
	}

	var (
		reached bool
		read    int64
		readErr error
	)
	// The probe stands in for the archive handler. It parses nothing: what it reports is what the
	// ROUTE handed it — how many bytes it could read and how the reading ended.
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		read, readErr = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})

	s := New(&Config{Address: "127.0.0.1", Port: "8081"})
	s.SetTechCardArchiveUploadHandler(probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	router, err := s.setupHTTPAPI(ctx, &auth.Server{})
	if err != nil {
		t.Fatalf("the production router must assemble: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/techcard-archive/upload",
		&tcapBody{left: maxImportArchiveBodyBytes + 1})
	req.Header.Set("Content-Type", "application/zip")
	router.ServeHTTP(httptest.NewRecorder(), req)

	// Positive control. Everything below is a statement about what the handler was handed, and it
	// says nothing at all if the request never arrived — a renamed path or a mount that stopped
	// matching would otherwise read as "the cap held".
	if !reached {
		t.Fatal("the request never reached the archive handler: the route under test is not the " +
			"route the server mounts (path, method or the /api group)")
	}

	var tooBig *http.MaxBytesError
	if !errors.As(readErr, &tooBig) {
		t.Fatalf("the import route must cap the request body: read %d bytes and the read ended with "+
			"%v, so nothing stopped a body one byte over the format ceiling. The cap belongs on the "+
			"ROUTE (limitBody(maxImportArchiveBodyBytes)) — a handler cannot install one on a body "+
			"it has already begun to read", read, readErr)
	}
	if tooBig.Limit != maxImportArchiveBodyBytes {
		t.Errorf("the route caps at %d bytes, want the format's %d (FORMAT.md §1.3)",
			tooBig.Limit, maxImportArchiveBodyBytes)
	}
	if read != maxImportArchiveBodyBytes {
		t.Errorf("the handler read %d bytes before the cap fired, want %d",
			read, maxImportArchiveBodyBytes)
	}
}
