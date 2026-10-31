// Package memoryschedule provides an in-process [schedule.Driver] for local
// development and single-replica deployments. It is single-process only:
// every replica runs every entry. Pending work from a running generation is
// discarded when its timers stop, and all work is lost on process exit.
// It uses the time package, so tests can run it inside
// testing/synctest.
//
// New accepts Builder callbacks for its shared logger. They run directly once
// in order on one fresh draft. Nil entries panic; nil/empty slice expansion
// supplies no edits. Retained drafts cannot change the driver after publication.
//
//	scheduler := schedule.New(memoryschedule.New())
//	compose.Run(scheduler.Run)
//
// Cancellation stops admission and drains already-started work on contexts
// detached from runner cancellation. Draining has no driver timeout. Entry
// timeout defaults to unlimited; set a positive cooperative execution timeout
// and bound task I/O rather than expecting cancellation of Run to stop handlers.
// Application shutdown can time out its runner wait and proceed to cleanup
// while an execution remains active. Concurrent Run is rejected through drain.
// Submissions accepted after timer admission closes wait pending for a later Run.
package memoryschedule
