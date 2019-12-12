package main

import (
	"context"
	"errors"
	"time"
)

type report struct {
	clients          Clients
	pendingResponses map[requestID][]Response
	respCh           chan Response

	requests   int // Number of requests
	errors     int // Number of errors
	mismatched int // Number of mismatched responses
	completed  int // Number of completed responses across clients
	overloaded int // Number of times reporting channel was overloaded

	elapsed time.Duration // Total duration of requests

	MismatchedResponse func([]Response)
}

// Report creates reporting by comparing responses from clients. It installs
// itself as the ResponseHandler to achieve this and will error if the
// ResponseHandler is already set.
func Report(clients Clients) (*report, error) {
	r := &report{
		clients:          clients,
		pendingResponses: make(map[requestID][]Response),
		respCh:           make(chan Response, chanBuffer),
	}

	responseHandler := func(resp Response) {
		select {
		case r.respCh <- resp:
		default:
			// Detected overloaded report channel, count it and try again.
			// If this happens too much, we need to tweak the
			// buffering/goroutines. since it will affect the benchmark results.
			r.overloaded += 1
			r.respCh <- resp
		}
	}

	for _, c := range clients {
		if c.ResponseHandler != nil {
			return nil, errors.New("failed to install report ResponseHandler on client, already set")
		}
		c.ResponseHandler = responseHandler
	}

	return r, nil
}

func (r *report) count(err error, elapsed time.Duration) {
	r.requests += 1
	if err != nil {
		r.errors += 1
	}
	r.elapsed += elapsed
}

func (r *report) compareResponses(resp Response) {
	// Are we waiting for more responses?
	if len(r.pendingResponses[resp.ID]) < len(r.clients)-1 {
		r.pendingResponses[resp.ID] = append(r.pendingResponses[resp.ID], resp)
		return
	}

	// All set, let's compare
	otherResponses := r.pendingResponses[resp.ID]
	delete(r.pendingResponses, resp.ID) // TODO: Reuse these arrays
	r.completed += 1

	durations := make([]time.Duration, 0, len(r.clients))
	durations = append(durations, resp.Elapsed)

	for _, other := range otherResponses {
		durations = append(durations, other.Elapsed)

		if !other.Equal(resp) {
			// Mismatch found, report the whole response set
			r.mismatched += 1
			if r.MismatchedResponse != nil {
				otherResponses = append(otherResponses, resp)
				r.MismatchedResponse(otherResponses)
			}
		}
	}

	logger.Debug().Int("id", int(resp.ID)).Msgf("converged responses, durations: %v", durations)
}

func (r *report) handle(resp Response) error {
	r.count(resp.Err, resp.Elapsed)
	r.compareResponses(resp)
	return nil
}

func (r *report) Serve(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case resp := <-r.respCh:
			if err := r.handle(resp); err != nil {
				return err
			}
		}
	}
}
