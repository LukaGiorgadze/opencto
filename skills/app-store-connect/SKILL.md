---
name: app-store-connect
description: >
    App Store Connect and `asc` CLI operations for iOS, macOS, tvOS, and visionOS apps.
    Use for: app records, signing/certificates, Xcode builds and uploads (trigger on macOS
    for any "build my app" request), TestFlight, metadata/screenshots/localization, ASO,
    What's New notes, App Store submission, pricing, subscriptions, IAPs, RevenueCat sync,
    macOS notarization, crash triage etc.
---

# App Store Connect

Use this umbrella skill as the entry point for App Store Connect work. It is a routing and safety guide; the detailed workflows live in `references/<skill-name>/SKILL.md`.

Do not load every reference by default. Classify the task, read the relevant reference skills, then execute with the current local `asc` command surface.

## Core Workflow

1. Identify the goal: build, beta, metadata, submission, pricing, diagnostics, app creation, signing, or automation.
2. Read `references/asc-cli-usage/SKILL.md` before designing or running non-trivial `asc` commands.
3. Read `references/asc-id-resolver/SKILL.md` when any command needs app, version, build, group, tester, subscription, IAP, or review IDs.
4. Read the task-specific reference skills from the map below.
5. Prefer JSON output for automation and table/markdown output for user-facing checks.
6. Validate or dry-run before remote writes whenever the command supports it.
7. Verify the resulting App Store Connect state before reporting success.

## Reference Map

| Reference                                           | Read when the user asks to...                                                                                                                                             |
| --------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `references/asc-cli-usage/SKILL.md`                 | Run or design `asc` commands, choose flags, handle auth, pagination, output formats, canonical verbs, or timeouts.                                                        |
| `references/asc-id-resolver/SKILL.md`               | Resolve human names, bundle IDs, version strings, build numbers, TestFlight groups, testers, or review submissions into deterministic App Store Connect IDs.              |
| `references/asc-workflow/SKILL.md`                  | Create, validate, run, dry-run, resume, or audit `.asc/workflow.json` repo-local automation graphs.                                                                       |
| `references/asc-app-create-ui/SKILL.md`             | Create a new App Store Connect app record through browser automation because the public API cannot create apps.                                                           |
| `references/asc-signing-setup/SKILL.md`             | Create or rotate bundle IDs, capabilities, certificates, provisioning profiles, devices, or encrypted signing sync.                                                       |
| `references/asc-xcode-build/SKILL.md`               | Build from source, manage Xcode version/build numbers, archive, export IPA/PKG artifacts, upload builds, or publish from local artifacts.                                 |
| `references/asc-build-lifecycle/SKILL.md`           | Find the latest or next build number, wait for build processing, inspect build state, distribute a processed build, or expire old TestFlight builds.                      |
| `references/asc-testflight-orchestration/SKILL.md`  | Manage TestFlight groups, testers, invitations, build distribution, beta config export, or What to Test notes.                                                            |
| `references/asc-crash-triage/SKILL.md`              | Fetch and summarize TestFlight crashes, beta feedback, screenshots, hangs, disk writes, launches, or other performance diagnostics.                                       |
| `references/asc-metadata-sync/SKILL.md`             | Pull, edit, validate, dry-run, push, or migrate App Store metadata and localizations in the canonical `./metadata` layout.                                                |
| `references/asc-localize-metadata/SKILL.md`         | Translate App Store metadata into additional locales with LLM-assisted, review-before-upload localization.                                                                |
| `references/asc-aso-audit/SKILL.md`                 | Audit canonical metadata for ASO issues, keyword waste, character usage, cross-locale keyword gaps, or Astro MCP keyword opportunities.                                   |
| `references/asc-whats-new-writer/SKILL.md`          | Write or localize App Store What's New release notes or promotional text from git logs, bullets, or rough text.                                                           |
| `references/asc-screenshot-resize/SKILL.md`         | Resize, sanitize, strip alpha, validate, or upload existing App Store screenshots using current `asc screenshots sizes` data.                                             |
| `references/asc-shots-pipeline/SKILL.md`            | Build/run an iOS simulator, drive UI with AXe, capture screenshots, frame them with Koubou, review, and upload.                                                           |
| `references/asc-release-flow/SKILL.md`              | Answer "can I submit now?", stage a release, handle first-time blockers, attach builds/items, submit for review, or monitor/cancel review.                                |
| `references/asc-submission-health/SKILL.md`         | Run preflight readiness checks, troubleshoot submission errors, verify encryption/content rights/screenshots/review details, submit prepared versions, or monitor review. |
| `references/asc-ppp-pricing/SKILL.md`               | Create or change territory-specific subscription/IAP pricing, PPP rollouts, price imports, price schedules, or pricing summaries.                                         |
| `references/asc-subscription-localization/SKILL.md` | Bulk-create or update subscription group, subscription, or IAP display-name localizations across App Store locales.                                                       |
| `references/asc-revenuecat-catalog-sync/SKILL.md`   | Audit or sync ASC subscriptions/IAPs with RevenueCat apps, products, entitlements, offerings, and packages.                                                               |
| `references/asc-notarization/SKILL.md`              | Archive, Developer ID export, zip, submit, monitor, log, staple, or troubleshoot notarization for macOS distribution outside the App Store.                               |
| `references/asc-wall-submit/SKILL.md`               | Submit or update an app entry in the App-Store-Connect-CLI Wall of Apps.                                                                                                  |

## Common Routes

For a new app from source, usually read:

1. `asc-cli-usage`
2. `asc-signing-setup`
3. `asc-app-create-ui` if the App Store Connect app record does not exist
4. `asc-xcode-build`
5. `asc-metadata-sync`
6. `asc-shots-pipeline` or `asc-screenshot-resize`
7. `asc-submission-health` and `asc-release-flow`

For a normal App Store release, usually read:

1. `asc-id-resolver`
2. `asc-xcode-build`
3. `asc-build-lifecycle`
4. `asc-metadata-sync` or `asc-whats-new-writer`
5. `asc-release-flow`
6. `asc-submission-health`

For a beta rollout, usually read:

1. `asc-id-resolver`
2. `asc-xcode-build` if a new build is needed
3. `asc-build-lifecycle`
4. `asc-testflight-orchestration`
5. `asc-crash-triage` after testers have feedback or crashes

For store listing work, usually read:

1. `asc-metadata-sync`
2. `asc-localize-metadata` for translations
3. `asc-aso-audit` for keyword and ASO checks
4. `asc-whats-new-writer` for release notes
5. `asc-screenshot-resize` or `asc-shots-pipeline` for screenshots

For monetization work, usually read:

1. `asc-id-resolver`
2. `asc-ppp-pricing`
3. `asc-subscription-localization`
4. `asc-revenuecat-catalog-sync` when RevenueCat is involved
5. `asc-release-flow` for first-review subscription or IAP blockers

For macOS distribution outside the App Store, read `asc-notarization`. For macOS App Store submission, use `asc-xcode-build`, then `asc-release-flow` and `asc-submission-health`.

## Safety Rules

- Treat App Store Connect operations as production operations.
- Confirm authentication context before writes: `asc auth status` or the relevant `ASC_*` environment.
- Use explicit long flags and deterministic IDs. Avoid relying on "latest" unless the user explicitly wants it or the reference says it is safe.
- Use `--paginate` for list commands that feed automation.
- Use `--dry-run`, validation, or preview commands before changing metadata, pricing, submissions, workflows, screenshots, RevenueCat mappings, or signing assets.
- Require explicit user approval before review submission, app creation final click, price changes, certificate/profile revocation, build expiration, RevenueCat writes, privacy publish, or other hard-to-undo mutations.
- Never store App Store Connect browser cookies. Browser UI automation must use a visible local session and allow the user to complete login and 2FA.
- Treat `asc web ...` commands as experimental web-session escape hatches for gaps in the public API. Offer a manual fallback when appropriate.
- Do not claim success from command completion alone. Verify by reading the resulting App Store Connect state.

## Verification

Use the most specific verification available:

- Builds: `asc builds info --build-id "BUILD_ID"` and confirm processing/validity.
- Metadata: `asc metadata validate`, `asc metadata push --dry-run`, then read/list the edited fields.
- Screenshots: `asc screenshots validate` and list uploaded screenshots after upload.
- TestFlight: list groups/testers/build attachments after distribution.
- Submissions: `asc validate`, `asc status`, `asc submit status`, or review submission list/detail commands.
- Pricing: pricing summary or schedule views before and after apply.
- RevenueCat sync: re-read ASC and RevenueCat catalogs and report created/skipped/failed items.
- Notarization: status/log commands, then staple verification when stapling.

## Notes

- This skill pack is for the unofficial community `asc` CLI and is not affiliated with Apple.
- Always check `--help` for exact flags because the CLI evolves.
- Prefer the current canonical release path: `asc validate`, `asc release stage`, `asc review submit`, and `asc publish appstore`.
- Do not use removed legacy submission shortcuts when the reference skills say they are obsolete.
