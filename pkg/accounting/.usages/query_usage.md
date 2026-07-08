Domain: querying stage-level consumption aggregates. Audience: `pkg/server` (calls Query); `cmd/afm`
(constructs the `Accountant` and wires it into `server.Config`).

## Constructing an Accountant

```go
accountant := accounting.NewAccountant(runDir, store, cfg.Pricing, cfg.Accounting.GetBucketMinutes())
// store: the same *state.Store instance pkg/server already holds in its Config
// cfg.Pricing: zero-value PricingConfig is fine — Query("cost", ...) just returns an empty slice
// cfg.Accounting.GetBucketMinutes(): 5 if the accounting.bucketMinutes config key is unset
```

## Querying aggregates

```go
aggregates, err := accountant.Query("tokens", "") // all stages, tokens metric
if err != nil {
    return err
}

aggregates, err = accountant.Query("cost", "design") // one stage, cost metric
// empty aggregates + nil err is a valid, expected result when no pricing: config is set —
// callers must not treat an empty slice as an error
```

## Handling the "pricing not configured" case

```go
aggregates, _ := accountant.Query("cost", "")
if len(aggregates) == 0 {
    // hide the money toggle in the API response / let the dashboard hide it client-side
}
```
