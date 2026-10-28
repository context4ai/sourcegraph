# Snapshot synchronization and retention

Synchronization fetches depth-one snapshots of the watched branch heads. The first sync does not download their parent history. Later syncs reuse existing Git objects and add newly observed heads; commits between synchronization runs are not automatically indexed.

To prepare a specific baseline, use **Create index** with its full lowercase 40-character commit SHA, or the prepare API with that revision. Missing snapshots are fetched directly from the registered repository, without deepening its full history. The Git server must permit fetching that SHA. A rejected request fails without substituting another commit. The branch selector identifies the watched scope; it does not prove ancestry for an explicitly supplied SHA.

Explicit SHA preparation pins that snapshot. In repository settings, **Pinned snapshots** lists the versions to keep independently of ordinary retention. Removing a SHA releases its pin; it becomes eligible for expiry at the next successful sync. Adding a pin alone does not fetch or index an absent snapshot: prepare the SHA first. A missing or failed index remains visible in task status.

Ordinary snapshots are retained by time since first observation, or by the configured count of most recently observed ordinary snapshots. Pins, current branch heads and active tasks are additionally protected. There are at most 4096 retained snapshots per path group. Commit choices show up to 20 observed snapshots, not the complete Git history.

After a successful sync, expired snapshots lose query eligibility. Git collection then removes objects no longer reachable from any retained snapshot, preserving the complete trees and available file objects of retained versions. Index collection removes retired generations separately after its grace period. Adding snapshots alone does not rewrite all packs; collection runs when versions expire and periodically during synchronization. No synchronization means no automatic snapshot expiry. **Clean old indexes** keeps branch-head and pinned indexes.

Each repository has a **Storage budget** (default 100 GiB, configurable from 1 GiB to 10 TiB) covering Git objects, index shards and active preparation files. Preparation stops when the sampled budget is reached. This is a protective budget, not an exact filesystem quota: checks occur between operations and during long-running work, so short-lived overshoot is possible. Increase the budget or release unused snapshots and clean indexes when capacity blocks preparation. Git compaction needs temporary working space and may exceed the repository budget while reclaiming objects; the global free-space reserve remains enforced.

`SOURCEGRAPH_MIN_FREE_BYTES` protects the instance's remaining disk space across repositories. Keep enough additional free capacity for replacement packs and index builds. Back up the control database, including pin and retention settings; Git snapshots and indexes reside on the instance volume.

Git 2.30 or newer is required for selective file hydration in path groups. The supplied production container includes a compatible Git version.
