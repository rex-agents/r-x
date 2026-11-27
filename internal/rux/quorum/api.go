package quorum

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// apiError is an error body from another authority's /quorum/* endpoint.
type apiError struct {
	Status int    `json:"-"`
	Code   string `json:"code"`
	Msg    string `json:"error"`
	Leader string `json:"leader,omitempty"`
}

var codeErrors = map[string]error{
	"not_leader":        ErrNotLeader,
	"stale_term":        ErrStaleTerm,
	"no_capacity":       ErrNoCapacity,
	"unknown_authority": ErrUnknownAuthority,
	"denied":            ErrVaultDenied,
	"missing":           ErrSecretMissing,
	"bad_code":          ErrBadCode,
	"no_leader":         ErrNoLeader,
}

func (e *apiError) Error() string { return e.Msg }
func (e *apiError) Unwrap() error { return codeErrors[e.Code] }

func errorCode(err error) string {
	for c, s := range codeErrors {
		if errors.Is(err, s) {
			return c
		}
	}
	return ""
}

func statusFor(code string) int {
	switch code {
	case "not_leader":
		return http.StatusMisdirectedRequest
	case "stale_term":
		return http.StatusConflict
	case "denied":
		return http.StatusForbidden
	case "missing", "unknown_authority":
		return http.StatusNotFound
	case "bad_code":
		return http.StatusUnauthorized
	case "no_capacity", "no_leader":
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error, leader string) {
	code := errorCode(err)
	writeJSON(w, statusFor(code), &apiError{Code: code, Msg: err.Error(), Leader: leader})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(v)
}

// call sends one JSON request to another authority's quorum endpoint, as this member.
func (n *Node) call(ctx context.Context, base, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, _ := json.Marshal(in)
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return err
	}
	for k, v := range n.dialHeader() {
		req.Header[k] = v
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		e := &apiError{Status: resp.StatusCode}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if json.Unmarshal(data, e) != nil || e.Msg == "" {
			e.Msg = fmt.Sprintf("%s %s: %s", method, path, resp.Status)
		}
		return e
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
