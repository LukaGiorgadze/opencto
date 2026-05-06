# Web Evidence

Use when a browser is the clearest proof of behavior.

Good fits include, but are not limited to:

- web apps, static sites, dashboards, docs, marketing pages, and admin UIs
- feature changes, bug fixes, loading states, forms, navigation, auth flows, and responsive layouts
- animations, transitions, media, charts, maps, canvases, and other rendered UI state
- browser-only behavior such as console errors, network requests, storage, cookies, accessibility state, and client-side routing

Use the `Browser` tool for web proof. The proof must come from a real browser
session, not from source inspection alone. Use exec commands for supporting
work such as starting a dev server or checking logs, but not as a substitute for
browser proof when the behavior is browser-visible.

Prefer a focused browser screenshot for visual proof. Use accessibility
snapshots, console output, network output, traces, videos, or logs when those
prove the requested behavior more directly than a screenshot. For animation or
state that changes over time, capture the stable end state, relevant intermediate
state, or video when a single screenshot would be misleading.

## Browser Guidance

- Open or reuse the relevant browser session with the `Browser` tool.
- Navigate to the page, route, story, preview, deployed URL, or local server that exercises the requested behavior.
- Interact with the page until the exact feature, fix, bug path, animation, log, or state is visible or observable.
- Wait for the page to finish rendering or for the target state to become stable before capturing proof.
- Use snapshots or semantic locators to find elements before interacting when selectors may be brittle.
- Re-check the state after interactions that navigate, mutate data, trigger async work, or start animations.
- Capture the smallest viewport or page area that still proves the behavior and preserves necessary context.
- Include browser console, network, storage, or log evidence when the proof depends on runtime side effects.

## Capture Guidance

- Show the requested state, not only app startup or a generic landing page.
- Include enough page context to identify where the proof was captured.
- Use desktop, mobile, or multiple viewport sizes when the request depends on responsive behavior.
- For visual regressions, capture before/after only when both are needed to prove the result.
- For errors or logs, keep the relevant message, request, status, stack, or identifier visible and trim unrelated noise.
- For flows, capture the final verified state plus any key intermediate state needed to show cause and effect.

## Proof Quality

Strong proof shows the requested browser state clearly and ties it to the user
request. It may include visible UI state, completed interaction, expected
console or network behavior, stable animation state, relevant route or URL,
viewport, request IDs, test data, or other identifiers.

Weak proof is clipped, still loading, missing the changed area, based only on
source code, missing the interaction that triggers the behavior, or only shows
that a server started when the requested behavior needed browser verification.
