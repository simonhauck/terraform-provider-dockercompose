# Drift details are visible, but apply still repairs them

The provider stored drift reasons in `config_hash` (`drifted: ...`) so a refresh would make the next plan propose an update. That made file drift say only that a file "was modified"; it did not show which lines changed.

We added a computed `drift` string that holds the full details, including a unified diff of the mirrored `compose.yaml` (truncated at 60 lines). `config_hash` keeps a compact first-line summary because it is what triggers the update; the detailed value is shown as the old side of the `drift` diff in the plan. The `.env` file is never diffed because it contains sensitive values.

Apply repairs drift and clears `drift`. To satisfy Terraform's consistency check, `ModifyPlan` marks `drift` unknown when an update will run, rather than planning the old details and then returning null from apply. When an update is not needed, the refreshed value is kept. This keeps the detailed view in refresh and plan output without leaving stale drift in state after a successful apply.
