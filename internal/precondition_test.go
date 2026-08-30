package internal

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// RFC 4918 §8.2 and §16: a request refused for a named precondition carries a
// DAV:error naming it, so the client can tell why rather than guessing from the
// status. The status alone was being returned.
func TestPreconditionErrorsCarryTheirElement(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
		want string
	}{
		{
			name: "PROPFIND Depth: infinity",
			err:  NewPreconditionError(http.StatusForbidden, PropFindFiniteDepthName),
			code: http.StatusForbidden,
			want: "propfind-finite-depth",
		},
		{
			name: "too many matches",
			err:  NewPreconditionError(http.StatusInsufficientStorage, NumberOfMatchesWithinLimitsName),
			code: http.StatusInsufficientStorage,
			want: "number-of-matches-within-limits",
		},
		{
			name: "destination already exists",
			err:  NewPreconditionError(http.StatusForbidden, ResourceMustBeNullName),
			code: http.StatusForbidden,
			want: "resource-must-be-null",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			ServeError(w, tc.err)

			if w.Code != tc.code {
				t.Errorf("code = %d, want %d", w.Code, tc.code)
			}
			body := w.Body.String()
			if !strings.Contains(body, "<error") {
				t.Errorf("no DAV:error element in the body:\n%s", body)
			}
			if !strings.Contains(body, tc.want) {
				t.Errorf("body does not name %s:\n%s", tc.want, body)
			}
		})
	}
}

func TestPropFindDepthResolution(t *testing.T) {
	depthOf := func(header string) (Depth, error) {
		r := httptest.NewRequest("PROPFIND", "/", http.NoBody)
		if header != "" {
			r.Header.Set("Depth", header)
		}
		return checkPropFindDepth(r)
	}

	t.Run("infinity carries the precondition", func(t *testing.T) {
		_, err := depthOf("infinity")
		if err == nil {
			t.Fatal("Depth: infinity was accepted")
		}
		w := httptest.NewRecorder()
		ServeError(w, err)
		if w.Code != http.StatusForbidden {
			t.Errorf("code = %d, want 403", w.Code)
		}
		if !strings.Contains(w.Body.String(), "propfind-finite-depth") {
			t.Errorf("no DAV:propfind-finite-depth in the body:\n%s", w.Body)
		}
	})

	t.Run("absent defaults to 1", func(t *testing.T) {
		depth, err := depthOf("")
		if err != nil {
			t.Fatal(err)
		}
		if depth != DepthOne {
			t.Errorf("depth = %v, want %v", depth, DepthOne)
		}
	})

	t.Run("unparseable is a bad request", func(t *testing.T) {
		_, err := depthOf("sideways")
		if err == nil {
			t.Fatal("an unparseable Depth was accepted")
		}
		if code := HTTPErrorFromError(err).Code; code != http.StatusBadRequest {
			t.Errorf("code = %d, want 400", code)
		}
	})

	t.Run("zero is honoured", func(t *testing.T) {
		depth, err := depthOf("0")
		if err != nil {
			t.Fatal(err)
		}
		if depth != DepthZero {
			t.Errorf("depth = %v, want %v", depth, DepthZero)
		}
	})
}
