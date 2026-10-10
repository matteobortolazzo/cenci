package incident

import (
	"bytes"
	"context"
	"encoding/json"
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

func (a AzureTelemetry) Collect(ctx context.Context, s Incident, r Resource) (string, error) {
	if !uuidPattern.MatchString(r.Workspace) || r.Query == "" {
		return "", fmt.Errorf("missing allowlisted telemetry workspace/query")
	}
	token, err := a.Credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{"https://api.loganalytics.io/.default"}})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]string{"query": r.Query, "timespan": s.FiredAt.Add(-15*time.Minute).Format(time.RFC3339) + "/" + s.FiredAt.Add(15*time.Minute).Format(time.RFC3339)})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.loganalytics.azure.com/v1/workspaces/"+r.Workspace+"/query", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token.Token)
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil {
		return "", err
	}
	if len(b) > 1024*1024 {
		return "", fmt.Errorf("telemetry exceeds 1 MiB; narrow configured query")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("read-only telemetry query returned HTTP %d", resp.StatusCode)
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
