# Deferred Linux validations

This is the append-only register for Linux checks deferred by the coordinator.
Later integration gates must append their own entries without replacing earlier
ones. An open entry is not a pass or waiver. Record the Linux host, source SHA,
exact commands, and results when closing an entry. Every entry must pass before
release gate I8 and before any public claim.

The dispatcher refuses to start while entries are open unless it is invoked with
`--defer-linux`. That option acknowledges the recorded deferral; it does not skip
checks. The I8 release gate remains blocked while any entry is open.

| Gate | Deferred Linux checks | Reason | Required closure | Status |
| --- | --- | --- | --- | --- |
| I1-public-foundation | Run the I1 integration's compatible component anchors from packages 01–22 with the v1.2 overlay on Linux, plus the integrated public CLI/project/Intent/approval/status foundation smoke. Active-Build, watch, and provider cases explicitly open for I3 remain outside this I1 selection. | No Linux runner is available on the Mac Studio. | Pass on a Linux benchmark host in the joint Go and TypeScript validation batch, before I8 and before any public claim. | OPEN |
