# AllRelay — scrcpy Parity Fix Spec

> Version: 0.1
> Date: 2026-06-19
> Status: Approved for implementation planning
> Scope: Screen popup lifecycle, keyboard parity, clipboard sharing, and screen sharpness

---

## 1. Background

AllRelay currently reuses the scrcpy Android server and transport model, but its Ubuntu-side control and screen UX are implemented differently:

- screen rendering/control is done in a browser popup via WebSocket + WebCodecs
- control input is produced by JavaScript in `allrelay-server/internal/web/static/app.js`
- live streams are coordinated by the Go backend in `allrelay-server/internal/web/` and `internal/transport/`

This creates several gaps compared with scrcpy:

1. the remote screen popup is aggressively re-focused and cannot stay minimized
2. keyboard handling does not correctly support Shift, modifiers, and printable symbols
3. clipboard sharing is not implemented end-to-end
4. screen rendering quality is visibly softer than scrcpy when enlarged

This document defines the target behavior, architecture, constraints, and acceptance criteria for fixing these gaps.

---

## 2. Goals

### 2.1 Product goals

- Make AllRelay remote-control UX feel much closer to scrcpy for everyday use
- Preserve the current browser-based architecture where practical
- Reuse scrcpy protocol semantics wherever possible instead of inventing new incompatible behavior

### 2.2 Engineering goals

- Minimize changes on the Android side unless strictly necessary
- Prefer protocol compatibility with the existing scrcpy server already embedded in this repo
- Keep the implementation decomposed so that popup lifecycle, input, clipboard, and rendering can be validated independently

### 2.3 Non-goals

- Full scrcpy feature parity in one pass
- Game-grade raw keyboard support for every browser/layout combination
- Perfect native-level clipboard autosync in both directions without browser permission constraints
- Replacing the browser renderer with a native SDL/OpenGL client in this milestone

---

## 3. Current-State Summary

### 3.1 Popup lifecycle

Current web UI behavior:

- `updateConnectionStatus()` calls `focusRemotePopup()` whenever screen is active
- status updates arrive repeatedly via WebSocket and periodic polling
- `focusRemotePopup()` calls `window.focus()`, which restores a minimized popup

Result: minimize does not stick.

### 3.2 Keyboard input

Current web input behavior:

- JavaScript builds scrcpy-style `INJECT_KEYCODE` binary packets
- `metaState` is always sent as `0`
- modifier keys are not fully mapped
- printable symbols such as `! @ # $ %` are dropped
- no `INJECT_TEXT` support exists in the browser client

Result: uppercase letters, symbols, and many layouts do not behave correctly.

### 3.3 Clipboard

Current clipboard behavior:

- Go has an older JSON clipboard concept in `internal/control/protocol.go`
- active remote control path uses raw binary packets over WebSocket to the scrcpy control TCP stream
- Android scrcpy server already supports clipboard messages
- AllRelay desktop side does not read scrcpy `DeviceMessage` clipboard events
- browser UI has no clipboard bridge

Result: clipboard sharing is effectively absent.

### 3.4 Screen sharpness

Current screen rendering behavior:

- WebCodecs decodes frames into a 2D canvas
- frames are drawn with `drawImage(frame, 0, 0)`
- CSS scales the canvas with `object-fit: contain`
- no explicit HiDPI canvas backing-store strategy exists
- decoder uses `optimizeForLatency: true`

Result: enlarged output appears softer than scrcpy, especially on HiDPI displays.

---

## 4. Cross-Cutting Design Decisions

### 4.1 Keep stream orchestration as-is

HTTP + JSON + WebSocket status/update messages remain the control plane for:

- device discovery
- connect/disconnect
- stream toggles
- metrics and UI state

### 4.2 Use scrcpy binary control semantics for live input features

For remote screen control, AllRelay must align with the scrcpy control protocol rather than extending the deprecated JSON control path.

This means the browser/Go path will treat the scrcpy protocol as the source of truth for:

- `INJECT_KEYCODE`
- `INJECT_TEXT`
- `INJECT_TOUCH_EVENT`
- `GET_CLIPBOARD`
- `SET_CLIPBOARD`
- `SET_DISPLAY_POWER`
- incoming scrcpy `DeviceMessage` clipboard events

### 4.3 Browser constraints are real and must be surfaced in UX

Unlike native scrcpy, a browser cannot always:

- read the system clipboard without a user gesture
- write to the system clipboard silently in all browsers and states
- monitor OS clipboard changes continuously in the background

Therefore the clipboard spec below separates:

- **automatic device → browser propagation**
- **best-effort browser → system clipboard apply**
- **explicit user-triggered system clipboard → device paste**

---

## 5. Workstream A — Screen Popup Lifecycle

### 5.1 Problem statement

When the screen stream is active, the dashboard forces focus back to the remote popup on repeated status updates. This makes minimize unusable.

### 5.2 Target behavior

- Opening the screen stream should open the popup once
- The popup may be focused once at creation time
- If the user minimizes the popup, it must remain minimized
- If the user manually re-focuses the popup, AllRelay must not fight that choice
- If the popup is explicitly closed while screen is active, screen stream shutdown behavior must remain intentional and predictable

### 5.3 Design

#### A1. Remove recurring auto-focus from state sync

`focusRemotePopup()` must not be called from periodic status reconciliation.

Allowed focus events:

- first popup creation
- explicit user action such as clicking “Open Remote” again

Disallowed focus events:

- WebSocket status updates
- metrics refreshes
- polling refreshes

#### A2. Separate popup existence from popup focus

The UI state model must distinguish:

- popup exists
- popup focused
- popup closed

Only existence should drive stream ownership logic.

#### A3. Preserve close-to-stop behavior only for true popup close

If the popup window is actually closed, AllRelay may still stop the screen stream if that is the intended UX. But this must not trigger on minimize.

### 5.4 Implementation notes

Likely touchpoints:

- `allrelay-server/internal/web/static/app.js`
  - `updateConnectionStatus()`
  - `openRemotePopup()`
  - `focusRemotePopup()`
  - `checkRemotePopupLifecycle()`

### 5.5 Acceptance criteria

- Minimizing the remote popup keeps it minimized for at least 30 seconds while screen/mic/speaker metrics continue updating
- Repeated WebSocket status updates do not restore a minimized popup
- Closing the popup still results in the documented screen-stream behavior
- Starting screen from the dashboard still opens the popup successfully

---

## 6. Workstream B — Keyboard Parity with scrcpy

### 6.1 Problem statement

The browser client currently sends only partial `INJECT_KEYCODE` events and always uses `metaState=0`. This breaks:

- uppercase letters
- Shift-modified symbols
- many modifier combinations
- punctuation on non-trivial layouts

### 6.2 Target behavior

AllRelay must correctly support at least the following in remote mode:

- lowercase letters
- uppercase letters via Shift
- number-row symbols such as `! @ # $ % ^ & * ( )`
- common punctuation (`- = [ ] \\ ; ' , . / \\``)
- navigation keys, backspace, tab, enter, escape, arrows, home/end/page up/down
- left/right Shift, Ctrl, Alt, Meta as modifiers
- common shortcuts using Ctrl/Meta where possible

### 6.3 Design choice: mixed input mode

AllRelay should follow a **mixed** model inspired by scrcpy:

#### B1. Non-printable and navigation keys

Use scrcpy `INJECT_KEYCODE` packets with:

- Android keycode
- action down/up
- repeat count
- computed `metaState`

This path covers:

- modifiers
- arrows/navigation
- backspace/delete/tab/enter/escape
- function-style control keys supported by the existing mapping

#### B2. Printable text entry

For printable characters, prefer scrcpy `INJECT_TEXT` when:

- no Ctrl/Alt/Meta command chord is active
- the event represents user text input
- the browser provides a reliable printable value

This path covers:

- uppercase characters
- symbols requiring Shift
- layout-sensitive printable characters
- better parity for text fields

#### B3. Command shortcuts

When Ctrl/Alt/Meta is involved, prefer `INJECT_KEYCODE` with proper `metaState` instead of text injection.

Examples:

- Ctrl+C
- Ctrl+V
- Ctrl+A
- Alt+Tab is out of scope and may stay browser-reserved

### 6.4 Modifier state model

The browser client must maintain current modifier state and convert it to Android metastate flags similar to scrcpy.

Minimum supported metastate bits:

- Shift left/right and aggregate Shift
- Ctrl left/right and aggregate Ctrl
- Alt left/right and aggregate Alt
- Meta left/right and aggregate Meta
- CapsLock if browser state is available
- NumLock if browser state is available

### 6.5 Mapping requirements

#### Required keycode mappings

At minimum, the browser side must map:

- left/right Shift
- left/right Ctrl
- left/right Alt
- left/right Meta
- digits 0-9
- letters A-Z keycodes
- punctuation keys for raw fallback
- navigation/editing keys

#### Required text path

The browser side must add a serializer for scrcpy `INJECT_TEXT` messages.

### 6.6 Browser event handling rules

- Keydown/keyup continue to drive raw key events
- Printable text injection may use `beforeinput`, `input`, or carefully filtered `keydown` based on browser behavior
- Composition/IME input is out of scope for the first pass, but dead keys must not corrupt internal modifier state

### 6.7 Implementation notes

Likely touchpoints:

- `allrelay-server/internal/web/static/app.js`
  - keyboard listeners
  - `mapBrowserKeyToAndroid()`
  - `buildKeyControlMessage()`
  - new `buildInjectTextControlMessage()`
- scrcpy reference:
  - `scrcpy/app/src/keyboard_sdk.c`
  - `scrcpy/app/src/control_msg.h`

### 6.8 Acceptance criteria

On a standard US keyboard in remote mode:

- typing `abcABC` on the PC produces `abcABC` on Android
- typing `!@#$%^&*()` produces the same characters on Android
- backspace, enter, tab, and arrow keys work correctly
- holding Shift while pressing letters behaves correctly
- `Ctrl+A`, `Ctrl+C`, and `Ctrl+V` reach Android apps where browser security allows the keys to be captured
- modifier state does not get stuck after rapid key sequences

---

## 7. Workstream C — Clipboard Sharing

### 7.1 Problem statement

The Android scrcpy server already supports clipboard sync, but AllRelay desktop/web does not consume or emit the required protocol messages end-to-end.

### 7.2 Constraints

Browser security constraints prevent native-scrcpy-style universal autosync. Therefore this workstream must provide a pragmatic browser-compatible model.

### 7.3 Target behavior

#### Device → desktop

- When Android clipboard changes, AllRelay receives the scrcpy device clipboard event
- The remote UI displays the latest device clipboard contents
- If browser clipboard write permission is available, AllRelay should best-effort copy it to the system clipboard
- If clipboard write is not possible automatically, the UI must expose a one-click “Copy device clipboard” action

#### Desktop → device

- The user can paste current desktop clipboard contents to Android from the remote UI
- The user can trigger this through an explicit UI action and optionally a keyboard shortcut where browser rules permit

### 7.4 Design

#### C1. Add scrcpy DeviceMessage reader on the Go side

The Go control path must parse incoming scrcpy device messages from the Android control socket, at minimum:

- `TYPE_CLIPBOARD`
- `TYPE_ACK_CLIPBOARD` (optional but recommended)

#### C2. Fan out clipboard updates to web clients

Clipboard events from Android should be forwarded to the browser over WebSocket as structured JSON UI events.

Suggested UI event type:

- `clipboard_update`

#### C3. Add browser control serializers for clipboard requests

The browser client must support scrcpy control messages for:

- `GET_CLIPBOARD`
- `SET_CLIPBOARD`

#### C4. UI/UX requirements

The remote page must expose clipboard controls:

- view latest device clipboard text
- copy device clipboard to local clipboard
- send local clipboard to device

#### C5. Permissions/fallback behavior

- Reading local system clipboard must be user-gesture initiated
- Writing local clipboard may be attempted automatically, but must degrade gracefully
- If permission fails, show a visible UI action instead of silently dropping clipboard content

### 7.5 Explicit scope choice

This milestone does **not** promise continuous automatic desktop → device clipboard monitoring in the browser. That can be considered only if AllRelay later ships a native desktop shell.

### 7.6 Implementation notes

Likely touchpoints:

- `allrelay-server/internal/web/controller.go`
- `allrelay-server/internal/web/hub.go`
- `allrelay-server/internal/web/static/app.js`
- possibly new Go parser helpers for scrcpy `DeviceMessage`
- scrcpy reference:
  - `scrcpy/server/src/main/java/com/genymobile/scrcpy/control/Controller.java`
  - `scrcpy/app/src/device_msg.c`
  - `scrcpy/app/src/input_manager.c`

### 7.7 Acceptance criteria

- Copying text on Android causes AllRelay UI to show the new clipboard content
- If browser permissions allow, the local system clipboard is updated automatically; otherwise a visible manual action is offered
- Clicking “Paste local clipboard to device” sends current local clipboard text to Android successfully
- Large clipboard contents fail gracefully with user-visible feedback if limits are exceeded

---

## 8. Workstream D — Screen Sharpness and Scaling Quality

### 8.1 Problem statement

The current browser renderer relies on simple canvas drawing plus CSS scaling, producing visibly softer output than scrcpy when enlarged.

### 8.2 Target behavior

- Remote screen should look materially sharper when enlarged
- HiDPI displays must not introduce avoidable extra blur
- Aspect ratio must remain correct
- Rendering changes must not break touch coordinate mapping

### 8.3 Design

#### D1. Own the scaling in canvas, not in CSS alone

The remote renderer must compute the destination rectangle and draw into the canvas backing store explicitly.

The canvas backing store should be sized using:

- rendered viewport width in CSS pixels × `devicePixelRatio`
- rendered viewport height in CSS pixels × `devicePixelRatio`

The actual frame should then be drawn into that backing store with aspect-preserving fit math.

#### D2. Enable high-quality canvas scaling

The renderer must explicitly set:

- `imageSmoothingEnabled = true`
- `imageSmoothingQuality = 'high'`

#### D3. Reduce CSS raster scaling responsibility

The CSS should no longer be the primary scaler of already-rasterized frame pixels.

The canvas element may still fill the popup, but sharpness should come from the backing-store size and explicit draw dimensions.

#### D4. Revisit decoder latency-vs-quality flag

`optimizeForLatency: true` should be re-evaluated.

Spec decision:

- default to visual quality unless latency regression is proven unacceptable
- if necessary, keep this as a configurable tuning flag for future work

#### D5. Keep touch mapping correct after renderer changes

Pointer coordinate mapping must continue to map browser pointer position to the underlying Android frame resolution, including letterboxing/pillarboxing regions.

### 8.4 Optional second-phase improvements

If the first pass is not sufficient, phase 2 may explore:

- OffscreenCanvas
- WebGL/WebGPU texture rendering
- explicit nearest/linear mode toggle for debugging
- a “100% / Fit” display mode toggle

These are optional and not required for the first implementation pass.

### 8.5 Implementation notes

Likely touchpoints:

- `allrelay-server/internal/web/static/app.js`
  - decoder output callback
  - canvas resize logic
  - pointer coordinate mapping
- `allrelay-server/internal/web/static/style.css`
- scrcpy reference:
  - `scrcpy/app/src/texture.c`
  - `scrcpy/app/src/screen.c`

### 8.6 Acceptance criteria

- On a HiDPI display, the remote screen appears visibly sharper than before at the same popup size
- Enlarging the popup does not introduce avoidable extra blur from CSS-only scaling
- Pointer/touch coordinates remain correct across the full visible image area
- Decoder/renderer changes do not materially regress stability

---

## 9. Delivery Order

Recommended implementation order:

1. **Workstream A — popup lifecycle**
   - smallest change, immediate UX win
2. **Workstream B — keyboard parity**
   - required for practical remote control
3. **Workstream C — clipboard sharing**
   - shares protocol work with B
4. **Workstream D — screen sharpness**
   - independent visual quality pass after control UX is stable

---

## 10. Validation Matrix

| Area | Manual validation | Automated validation potential |
|------|-------------------|--------------------------------|
| Popup lifecycle | Minimize/restore/close scenarios | Limited; mostly browser/manual |
| Keyboard | US-layout input script and app form testing | Partial unit coverage for mapping/serialization |
| Clipboard | Android copy/paste and browser permission cases | Partial parser/serializer tests |
| Sharpness | Visual comparison before/after on HiDPI + standard DPI | Limited; can unit test geometry math |

---

## 11. Implementation Guardrails

- Do not extend the deprecated JSON control protocol for new live-control features
- Prefer adding small, testable serializers/parsers for scrcpy-compatible binary messages
- Keep browser fallbacks explicit when permissions block an operation
- Avoid introducing native-only assumptions into the browser UX spec

---

## 12. Definition of Done

This spec is considered fully implemented when:

- popup minimize behavior no longer fights the user
- keyboard entry supports normal uppercase and symbol typing in remote mode
- clipboard sharing works end-to-end within browser permission constraints
- enlarged rendering is visibly sharper and pointer mapping remains correct
- changes are documented in user-facing usage docs if the UX changes materially
