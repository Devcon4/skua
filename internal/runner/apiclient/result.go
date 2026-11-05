package apiclient

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Devcon4/skua/internal/contract/wire"
)

// SubmitResult waits until every emitted event is acked, then posts r until skua-api answers 200. It retries
// network errors and 5xx, returns ErrTooLarge on 413, ErrRunUnknown when unsent events have no run, and an
// error for any other status.
func (c *Client) SubmitResult(ctx context.Context, r wire.RunnerResult) error {
	if err := c.flush(ctx); err != nil {
		return err
	}
	body, err := marshal(r)
	if err != nil {
		return fmt.Errorf("apiclient: encode result: %w", err)
	}
	backoff := c.opt.retryBackoff()
	for {
		err := c.postResult(ctx, body)
		if err == nil || !retryable(err) {
			return err
		}
		c.opt.log.Warn("runner result not stored, retrying", "err", err)
		if !sleep(ctx, backoff.Step()) {
			return ctx.Err()
		}
	}
}

func (c *Client) postResult(ctx context.Context, body []byte) error {
	resp, err := c.do(ctx, http.MethodPost, wire.RunnerResultPath, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusRequestEntityTooLarge:
		return fmt.Errorf("%w: %s", ErrTooLarge, readReason(resp.Body))
	default:
		return refusal(resp)
	}
}
