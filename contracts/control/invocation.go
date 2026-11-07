package control

import "context"

type meshInvocationKey struct{}

// WithMeshInvocation returns ctx carrying the bearer token an agent request was authenticated with: Wait decides the
// request against that invocation (still running, same attempt) under the Store lock.
func WithMeshInvocation(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, meshInvocationKey{}, token)
}

// MeshInvocation is the token WithMeshInvocation put in ctx, "" outside an agent request.
func MeshInvocation(ctx context.Context) string {
	token, _ := ctx.Value(meshInvocationKey{}).(string)
	return token
}
