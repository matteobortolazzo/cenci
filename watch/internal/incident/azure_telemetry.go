package incident

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

type AzureTelemetry struct {
	Credential azcore.TokenCredential
	Client     *http.Client
}

// errTelemetryTooLarge is the existing over-1 MiB note, shared by the worker and
// `cenci incident verify`.
var errTelemetryTooLarge = errors.New("telemetry exceeds 1 MiB; narrow configured query")

// errTelemetryTarget is the guard every workspace query passes: the workspace is
// a UUID, so it cannot reshape the request path, and the query is non-empty.
var errTelemetryTarget = errors.New("missing allowlisted telemetry workspace/query")

// tokenRequestError marks a failure to acquire the Log Analytics token, apart
// from a failure to reach the service. Its text is the credential's own.
type tokenRequestError struct{ err error }

func (e *tokenRequestError) Error() string { return e.err.Error() }
func (e *tokenRequestError) Unwrap() error { return e.err }

// queryWorkspace posts one read-only query to a Log Analytics workspace and
// returns the HTTP status and the body (at most 1 MiB). A nil client means the
// default 30-second client that follows no redirect. A workspace that is not a
// UUID, or an empty query, returns errTelemetryTarget before any token request
// or network use. A token failure is a
// *tokenRequestError. A body over 1 MiB returns errTelemetryTooLarge together
// with the status, ahead of any status judgment; the caller judges the status.
func queryWorkspace(ctx context.Context, cred azcore.TokenCredential, client *http.Client, workspace, query, timespan string) (int, []byte, error) {
	if !uuidPattern.MatchString(workspace) || query == "" {
		return 0, nil, errTelemetryTarget
	}
	token, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{LogAnalyticsScope}})
	if err != nil {
		return 0, nil, &tokenRequestError{err}
	}
	body, err := json.Marshal(map[string]string{"query": query, "timespan": timespan})
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.loganalytics.azure.com/v1/workspaces/"+workspace+"/query", bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token.Token)
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if len(b) > 1024*1024 {
		return resp.StatusCode, nil, errTelemetryTooLarge
	}
	return resp.StatusCode, b, nil
}

func (a AzureTelemetry) Collect(ctx context.Context, s Incident, r Resource) (string, error) {
	timespan := s.FiredAt.Add(-15*time.Minute).Format(time.RFC3339) + "/" + s.FiredAt.Add(15*time.Minute).Format(time.RFC3339)
	status, b, err := queryWorkspace(ctx, a.Credential, a.Client, r.Workspace, r.Query, timespan)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("read-only telemetry query returned HTTP %d", status)
	}
	var result struct {
		Error  json.RawMessage `json:"error"`
		Tables json.RawMessage `json:"tables"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return "", err
	}
	if len(result.Tables) == 0 || len(result.Error) > 0 && string(result.Error) != "null" {
		return "", fmt.Errorf("telemetry query incomplete or failed")
	}
	return string(b), nil
}
