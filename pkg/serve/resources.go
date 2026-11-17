package serve

import (
	"context"
	"errors"
	"net/url"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/pkg/mcpserver"
)

const resourcePrefix = "afm://runs/"

// registerResources offers the resource templates of a single run's endpoint for every run: the run_id inside
// the URI selects it.
func (g *Gateway) registerResources() {
	for _, tmpl := range mcpserver.ResourceTemplates() {
		g.server.AddResourceTemplate(tmpl, g.readResource)
	}
}

// runOfURI extracts the run_id from afm://runs/<run_id>/...
func runOfURI(uri string) (string, bool) {
	rest, ok := strings.CutPrefix(uri, resourcePrefix)
	if !ok {
		return "", false
	}
	escaped, _, _ := strings.Cut(rest, "/")
	runID, err := url.PathUnescape(escaped)
	return runID, err == nil && runID != ""
}

func (g *Gateway) resolveURI(ctx context.Context, uri string) (RunView, error) {
	runID, ok := runOfURI(uri)
	if !ok {
		return RunView{}, sdkmcp.ResourceNotFoundError(uri)
	}
	v, err := g.cfg.Inventory.Resolve(ctx, runID)
	if errors.Is(err, ErrRunNotFound) {
		return RunView{}, sdkmcp.ResourceNotFoundError(uri)
	}
	return v, err
}

func (g *Gateway) readResource(ctx context.Context, req *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
	v, err := g.resolveURI(ctx, req.Params.URI)
	if err != nil {
		return nil, err
	}
	return g.cfg.Sessions.ReadResource(ctx, v, req.Params.URI)
}

func (g *Gateway) subscribe(ctx context.Context, req *sdkmcp.SubscribeRequest) error {
	v, err := g.resolveURI(ctx, req.Params.URI)
	if err != nil {
		return err
	}
	g.watchHost(req.Session)
	if err := g.cfg.Sessions.Subscribe(ctx, v, req.Params.URI, req.Session); err != nil {
		return err
	}
	if g.subscribed != nil {
		g.subscribed()
	}
	return nil
}

// unrecordedSubscriber is a subscribe the SDK has not recorded yet; missed says an update was sent meanwhile.
type unrecordedSubscriber struct{ missed bool }

// resourceUpdated tells uri's subscribers that it may have changed. A subscribe still unrecorded is not among them,
// so it is marked first and told once it is recorded.
func (g *Gateway) resourceUpdated(uri string) {
	g.mu.Lock()
	for sub := range g.unrecorded[uri] {
		sub.missed = true
	}
	g.mu.Unlock()
	_ = g.server.ResourceUpdated(context.Background(), &sdkmcp.ResourceUpdatedNotificationParams{URI: uri})
}

// subscriptionKey is one host client's subscription to one URI.
type subscriptionKey struct {
	session *sdkmcp.ServerSession
	uri     string
}

// recordSubscribers runs subscribe and unsubscribe of one client and URI one at a time through the SDK's record of
// the subscriber, so an unsubscribe cannot overtake a subscribe that reached the run. An update sent before that
// record, a lost session's notice among them, is repeated once the subscriber is recorded.
func (g *Gateway) recordSubscribers(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
	return func(ctx context.Context, method string, req sdkmcp.Request) (sdkmcp.Result, error) {
		var key subscriptionKey
		switch r := req.(type) {
		case *sdkmcp.SubscribeRequest:
			if r.Params == nil {
				return next(ctx, method, req)
			}
			key = subscriptionKey{session: r.Session, uri: r.Params.URI}
		case *sdkmcp.UnsubscribeRequest:
			if r.Params == nil {
				return next(ctx, method, req)
			}
			key = subscriptionKey{session: r.Session, uri: r.Params.URI}
		default:
			return next(ctx, method, req)
		}
		unlock, err := g.lockSubscription(ctx, key)
		if err != nil {
			return nil, err
		}
		defer unlock()
		if _, subscribe := req.(*sdkmcp.SubscribeRequest); !subscribe {
			return next(ctx, method, req)
		}
		sub := g.beginSubscribe(key.uri)
		res, err := next(ctx, method, req)
		if g.endSubscribe(key.uri, sub) && err == nil {
			g.resourceUpdated(key.uri)
		}
		return res, err
	}
}

// lockSubscription waits until no other subscribe or unsubscribe of the same client and URI is in flight; the wait
// ends with ctx.
func (g *Gateway) lockSubscription(ctx context.Context, key subscriptionKey) (unlock func(), err error) {
	for {
		g.mu.Lock()
		busy, held := g.changing[key]
		if !held {
			done := make(chan struct{})
			if g.changing == nil {
				g.changing = map[subscriptionKey]chan struct{}{}
			}
			g.changing[key] = done
			g.mu.Unlock()
			return func() {
				g.mu.Lock()
				delete(g.changing, key)
				g.mu.Unlock()
				close(done)
			}, nil
		}
		g.mu.Unlock()
		select {
		case <-busy:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (g *Gateway) beginSubscribe(uri string) *unrecordedSubscriber {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.unrecorded == nil {
		g.unrecorded = map[string]map[*unrecordedSubscriber]struct{}{}
	}
	if g.unrecorded[uri] == nil {
		g.unrecorded[uri] = map[*unrecordedSubscriber]struct{}{}
	}
	sub := &unrecordedSubscriber{}
	g.unrecorded[uri][sub] = struct{}{}
	return sub
}

// endSubscribe forgets sub and reports whether an update was sent while it was unrecorded.
func (g *Gateway) endSubscribe(uri string, sub *unrecordedSubscriber) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.unrecorded[uri], sub)
	if len(g.unrecorded[uri]) == 0 {
		delete(g.unrecorded, uri)
	}
	return sub.missed
}

// watchHost frees a host session's subscriptions when it disconnects, once per session.
func (g *Gateway) watchHost(ss *sdkmcp.ServerSession) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.watched[ss] {
		return
	}
	if g.watched == nil {
		g.watched = map[*sdkmcp.ServerSession]bool{}
	}
	g.watched[ss] = true
	go func() {
		_ = ss.Wait()
		g.cfg.Sessions.ReleaseHost(ss)
		g.mu.Lock()
		delete(g.watched, ss)
		g.mu.Unlock()
	}()
}

func (g *Gateway) unsubscribe(ctx context.Context, req *sdkmcp.UnsubscribeRequest) error {
	v, err := g.resolveURI(ctx, req.Params.URI)
	if err != nil {
		return nil // the run is gone; there is nothing left to unsubscribe from
	}
	return g.cfg.Sessions.Unsubscribe(ctx, v, req.Params.URI, req.Session)
}
