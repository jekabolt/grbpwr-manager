package revalidation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
)

const (
	vercelApiUrl = "https://api.vercel.com/v6/deployments"
)

type Revalidator struct {
	c      *Config
	client *http.Client
}

type Config struct {
	ProjectId        string        `mapstructure:"project_id"`
	VercelApiToken   string        `mapstructure:"vercel_api_token"`
	RevalidateSecret string        `mapstructure:"revalidate_secret"`
	HTTPTimeout      time.Duration `mapstructure:"http_timeout"`
}

func New(ctx context.Context, c *Config) (*Revalidator, error) {
	timeout := c.HTTPTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	v := &Revalidator{
		c:      c,
		client: &http.Client{Timeout: timeout},
	}
	return v, nil
}

func (v *Revalidator) getDeployments(ctx context.Context) ([]*dto.Deployment, error) {
	url := fmt.Sprintf("%s?projectId=%s&state=READY&limit=3", vercelApiUrl, v.c.ProjectId)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create GET request to %s: %w", url, err)
	}

	req.Header.Add("Authorization", "Bearer "+v.c.VercelApiToken)

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make GET request to %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d from %s", resp.StatusCode, url)
	}
	var result dto.DeploymentResponse

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode deployments response from %s: %w", url, err)
	}

	deployments := make([]*dto.Deployment, len(result.Deployments))
	for i, d := range result.Deployments {
		deployments[i] = &d
	}
	return deployments, nil
}

func (v *Revalidator) revalidate(ctx context.Context, deploymentURL string, revalidationData *dto.RevalidationData) error {
	u := &url.URL{
		Scheme: "https",
		Host:   deploymentURL,
		Path:   "/api/revalidate",
	}
	q := u.Query()
	q.Set("secret", v.c.RevalidateSecret)
	u.RawQuery = q.Encode()
	apiUrl := u.String()

	payload, err := json.Marshal(revalidationData)
	if err != nil {
		return fmt.Errorf("failed to marshal revalidationData for deployment %s: %w", deploymentURL, err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiUrl, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to create POST request to %s: %w", apiUrl, err)
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := v.client.Do(req)
	if err != nil {
		slog.Default().Error("HTTP request failed", "deploymentURL", deploymentURL, "error", err)
		return fmt.Errorf("failed to POST to %s: %w", apiUrl, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Default().Error("Failed to read response body", "deploymentURL", deploymentURL, "error", err)
		return fmt.Errorf("failed to read response body from %s: %w", apiUrl, err)
	}

	if resp.StatusCode != 200 {
		slog.Default().Error("Revalidate endpoint returned non-200", "deploymentURL", deploymentURL, "status", resp.StatusCode, "body", string(body))
		return fmt.Errorf("revalidate failed for %s (status %d): %s", deploymentURL, resp.StatusCode, string(body))
	}

	slog.Default().Info("Revalidated", "deploymentURL", deploymentURL, "response", string(body))
	return nil
}

func (v *Revalidator) RevalidateAll(ctx context.Context, revalidationData *dto.RevalidationData) error {
	deployments, err := v.getDeployments(ctx)
	if err != nil {
		return fmt.Errorf("failed to get deployments: %w", err)
	}

	deployments = append(deployments, &dto.Deployment{
		URL: "https://grbpwr-com-dusky.vercel.app",
	}, &dto.Deployment{
		URL: "https://grbpwr.com",
	})

	const maxRetries = 3
	const maxConcurrent = 5

	errChan := make(chan error, len(deployments))
	semaphore := make(chan struct{}, maxConcurrent)

	var wg sync.WaitGroup
	for _, deployment := range deployments {
		wg.Add(1)
		go func(d *dto.Deployment) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			var revalidateErr error
			for attempt := 1; attempt <= maxRetries; attempt++ {
				slog.Default().Info("Revalidating", "deploymentURL", d.URL, "attempt", attempt)
				err := v.revalidate(ctx, d.URL, revalidationData)
				if err == nil {
					return
				}
				revalidateErr = err
				slog.Default().Error("Revalidation failed", "deploymentURL", d.URL, "attempt", attempt, "error", err)
				time.Sleep(time.Duration(attempt) * time.Second)
			}
			errChan <- fmt.Errorf("deployment %s: %w", d.URL, revalidateErr)
		}(deployment)
	}

	wg.Wait()
	close(errChan)

	var errs []error
	for err := range errChan {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		slog.Default().Error("revalidation failed for %d deployments: %w", len(errs), errors.Join(errs...))
	}

	return nil
}
