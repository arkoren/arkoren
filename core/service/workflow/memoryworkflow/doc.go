// Package memoryworkflow provides an in-process [workflow.Driver] for local
// development and single-replica deployments. It runs every run in a goroutine
// of the process, so runs are lost on restart and are not shared between
// replicas. It uses the system clock for sleeps, retries, timeouts, and
// delays; tests move time with testing/synctest.
// Definitions use [workflow.New] and [workflow.NewStep] builder callbacks;
// configure required [workflow.Builder.MaxDuration] before Handlers.
// Retained definition drafts cannot alter the published registry. Per-call
// step policy changes use [workflow.Step.Derive], not driver configuration.
//
// New keeps the registry positional and accepts Builder callbacks for its
// shared logger. They run directly once in order on one fresh draft. Nil
// entries panic; nil/empty slice expansion supplies no edits. Retained drafts
// cannot reconfigure the driver; registry handlers and loggers are not cloned.
//
// Start copies encoded input before launching the run, including delayed,
// child, and spawned runs. Signal copies each accepted value before buffering
// it. Callers may reuse these byte buffers after the call returns. Each Result
// read and child join returns independent encoded output, so mutating a read
// cannot change the retained result. Copies preserve the encoded bytes; they
// do not clone codecs, handlers, or values captured by application functions.
//
// Cancel and timeouts end a run by canceling its context; they cannot stop
// code that ignores it. [Driver.Stop] waits for workflow and step-attempt
// goroutines until its own context ends; an expired Stop cannot force them out.
// Attempt telemetry finishes when the handler returns, not when its supervisor
// stops waiting, so a late attempt can outlive the reported run outcome.
//
// Configure worker telemetry with [workflow.NewRegistry] and
// [workflow.RegistryBuilder.Telemetry], independently of the client's setting.
// Nil follows the current process default per actual attempt. The driver copies
// [workflow.StartOptions.Trace], forwards it to child/spawn runs, and extracts
// it onto each independently cancelable step context. It never retains the
// start caller's context or uses application metadata as a propagation carrier.
//
// During application composition:
//
//	registry := compose.Must(workflow.Handlers(Onboard, SendWelcome))
//	driver := memoryworkflow.New(registry)
//	compose.Cleanup(driver.Stop)
//
//	client := workflow.NewClient(driver)
package memoryworkflow
