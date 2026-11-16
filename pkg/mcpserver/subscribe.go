package mcpserver

import (
	"context"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// subscribable says which resources send updates; plans and artifacts are read on demand.
func (s *Service) subscribable(uri string) bool {
	return uri == statusURI(s.runID) || uri == costURI(s.runID)
}

// subscribeHandler starts the notifier the first time anyone subscribes, so a service nobody subscribes to
// runs no background work.
func (s *Service) subscribeHandler(_ context.Context, req *sdkmcp.SubscribeRequest) error {
	if !s.subscribable(req.Params.URI) {
		return sdkmcp.ResourceNotFoundError(req.Params.URI)
	}
	return s.ensureNotifier()
}

func (s *Service) unsubscribeHandler(context.Context, *sdkmcp.UnsubscribeRequest) error { return nil }
