#!/usr/bin/env python3
"""
winctl.py — Cross-platform window control. macOS · Linux · Windows.
Powered by PyWinCtl. Self-installs on first run.

Usage:
  python3 winctl.py list
  python3 winctl.py open     <AppName>
  python3 winctl.py front    <AppName>
  python3 winctl.py minimize <AppName>
  python3 winctl.py maximize <AppName>
  python3 winctl.py restore  <AppName>
  python3 winctl.py screenshot [--front] <AppName> [output.png]

Commands:
  list
      Show running apps and their visible, minimized, maximized, or hidden
      windows as PyWinCtl can see them.

  open <AppName>
      Launch an app if it is closed, or reopen it if it is already running
      with no visible windows. On macOS this handles the red-window-button
      state by sending the app a reopen event after open -a.
      Aliases: launch, start.

  front <AppName>
      Bring an already-running app/window to the front. If the app is running
      but has no visible windows, macOS activation is attempted. This does not
      guarantee a new window is created; use open for that.
      Aliases: activate, focus.

  minimize <AppName>
      Minimize the first matching window.
      Alias: min.

  maximize <AppName>
      Restore the first matching window if needed, then maximize it.
      Alias: max.

  restore <AppName>
      Return the first matching existing window from minimized/non-normal state
      to a normal visible state and focus it. This does not launch the app or
      create a new window.

  screenshot [--front] <AppName> [output.png]
      Capture the first matching window. With --front, restore/activate the
      window before capturing. If output.png is omitted, save under
      $OPENCTO_WORKSPACE/screenshots, or ~/.opencto/screenshots when the
      environment variable is not set.
      Aliases: snap, shot.
"""

import sys
import os
import subprocess
import platform
import time
from datetime import datetime

# ─── Auto-install pywinctl ────────────────────────────────────────────────────

def ensure_pywinctl():
    try:
        import pywinctl
    except ImportError:
        print("📦 Installing pywinctl...")
        subprocess.check_call(
            [sys.executable, "-m", "pip", "install", "pywinctl", "-q"],
            stdout=subprocess.DEVNULL
        )
        print("✅ pywinctl installed.\n")

ensure_pywinctl()
import pywinctl as pwc

# ─── Helpers ──────────────────────────────────────────────────────────────────

OS = platform.system()  # 'Darwin', 'Linux', 'Windows'

def die(msg):
    print(f"❌ {msg}", file=sys.stderr)
    sys.exit(1)

def ok(msg):
    print(f"✅ {msg}")

def info(msg):
    print(f"ℹ️  {msg}")

def find_windows(app_name: str):
    """
    Find all windows belonging to an app by name (case-insensitive).
    Tries exact match first, then substring match.
    """
    name_lower = app_name.lower()

    all_apps = pwc.getAllAppsWindowsTitles()  # {appName: [titles]}

    # Exact match
    for name, titles in all_apps.items():
        if name.lower() == name_lower:
            wins = []
            for title in titles:
                wins.extend(pwc.getWindowsWithTitle(title))
            return wins

    # Substring match
    for name, titles in all_apps.items():
        if name_lower in name.lower():
            wins = []
            for title in titles:
                wins.extend(pwc.getWindowsWithTitle(title))
            if wins:
                info(f"Matched '{name}' for query '{app_name}'")
                return wins

    return []

def find_app_name(app_name: str):
    """
    Find an app name as reported by PyWinCtl (case-insensitive).
    Tries exact match first, then substring match.
    """
    name_lower = app_name.lower()
    all_apps = pwc.getAllAppsWindowsTitles()  # {appName: [titles]}

    for name in all_apps.keys():
        if name.lower() == name_lower:
            return name

    for name in all_apps.keys():
        if name_lower in name.lower():
            info(f"Matched '{name}' for query '{app_name}'")
            return name

    return None

def get_first_window(app_name: str):
    wins = find_windows(app_name)
    if not wins:
        die(f"No windows found for '{app_name}'. Run 'list' to see open apps.")
    return wins[0]

def applescript_string(value: str) -> str:
    escaped = value.replace("\\", "\\\\").replace('"', '\\"')
    return f'"{escaped}"'

def default_screenshot_output(app_name: str) -> str:
    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    filename = f"{app_name.replace(' ', '_')}_{timestamp}.png"
    workspace = os.environ.get("OPENCTO_WORKSPACE")
    if not workspace:
        workspace = os.path.join(os.path.expanduser("~"), ".opencto")
    return os.path.join(workspace, "screenshots", filename)

# ─── Commands ─────────────────────────────────────────────────────────────────

def cmd_list():
    all_apps = pwc.getAllAppsWindowsTitles()  # {appName: [windowTitles]}

    if not all_apps:
        print("No open windows found.")
        return

    # Header
    print(f"{'APP NAME':<30} {'WINDOWS'}")
    print("─" * 90)

    for app_name in sorted(all_apps.keys(), key=str.lower):
        titles = all_apps[app_name]
        if not titles:
            print(f"{app_name:<30} (no open windows)")
            continue

        wins = []
        for title in titles:
            matched = pwc.getWindowsWithTitle(title)
            wins.extend(matched)

        if not wins:
            print(f"{app_name:<30} (no open windows)")
            continue

        labels = []
        for w in wins:
            t = w.title or "(untitled)"
            flags = []
            if w.isMinimized:  flags.append("minimized")
            if w.isMaximized:  flags.append("maximized")
            if not w.isVisible: flags.append("hidden")
            if flags:
                t += f" [{', '.join(flags)}]"
            labels.append(t)

        first = labels[0]
        print(f"{app_name:<30} {first}")
        for label in labels[1:]:
            print(f"{'':30} {label}")

def cmd_front(app_name: str):
    wins = find_windows(app_name)
    if wins:
        win = wins[0]
        if win.isMinimized:
            win.restore()
        win.activate()
        ok(f"Brought '{app_name}' to front")
        return

    app = find_app_name(app_name)
    if not app:
        die(f"No app found for '{app_name}'. Run 'list' to see open apps.")

    if OS == "Darwin":
        try:
            subprocess.run(
                ["osascript", "-e", f'tell application "{app}" to activate'],
                check=True,
            )
        except subprocess.CalledProcessError as exc:
            die(f"Could not activate '{app}' with osascript: {exc}")
        ok(f"Brought '{app}' to front")
        return

    die(f"No windows found for '{app}'. Run 'list' to see open apps.")


def cmd_open(app_name: str):
    app = find_app_name(app_name)
    if OS == "Darwin":
        _open_macos(app or app_name)
        return

    if app:
        cmd_front(app)
    elif OS == "Linux":
        _open_linux(app_name)
    elif OS == "Windows":
        _open_windows(app_name)
    else:
        die(f"Open not supported on {OS}")

def _open_macos(app_name: str):
    try:
        subprocess.run(["open", "-a", app_name], check=True)
    except subprocess.CalledProcessError as exc:
        die(f"Could not open '{app_name}' with open -a: {exc}")

    # Reopen handles the macOS state where an app is running but all windows
    # were closed with the red window button.
    script = f"""
tell application {applescript_string(app_name)}
    activate
    reopen
end tell
"""
    subprocess.run(
        ["osascript", "-e", script],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    )
    time.sleep(0.3)
    ok(f"Opened '{app_name}'")

def _open_linux(app_name: str):
    commands = []
    if _cmd_exists("gtk-launch"):
        commands.append(["gtk-launch", app_name])
    commands.append([app_name])

    for command in commands:
        try:
            subprocess.Popen(
                command,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                start_new_session=True,
            )
        except OSError:
            continue
        ok(f"Opened '{app_name}'")
        return

    die(f"Could not open '{app_name}'. Try the desktop file ID or executable name.")

def _open_windows(app_name: str):
    try:
        subprocess.Popen(
            ["powershell", "-NoProfile", "-Command", "Start-Process", app_name],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
    except OSError as exc:
        die(f"Could not open '{app_name}' with PowerShell: {exc}")
    ok(f"Opened '{app_name}'")

def cmd_minimize(app_name: str):
    win = get_first_window(app_name)
    win.minimize()
    ok(f"Minimized '{app_name}'")

def cmd_maximize(app_name: str):
    win = get_first_window(app_name)
    if win.isMinimized:
        win.restore()
    win.maximize()
    ok(f"Maximized '{app_name}'")

def cmd_restore(app_name: str):
    win = get_first_window(app_name)
    win.restore()
    win.activate()
    ok(f"Restored '{app_name}'")

def cmd_screenshot(args):
    bring_front = False
    if args and args[0] == "--front":
        bring_front = True
        args = args[1:]

    if not args:
        die("Usage: winctl.py screenshot [--front] <AppName> [output.png]")

    app_name = args[0]
    output = args[1] if len(args) > 1 else default_screenshot_output(app_name)
    output = os.path.expanduser(output)
    parent = os.path.dirname(output)
    if parent:
        os.makedirs(parent, exist_ok=True)

    win = get_first_window(app_name)

    if bring_front:
        info(f"Activating '{app_name}'...")
        if win.isMinimized:
            win.restore()
        win.activate()
        import time; time.sleep(0.6)

    _take_screenshot(win, app_name, output)

def _take_screenshot(win, app_name: str, output: str):
    if OS == "Darwin":
        _screenshot_macos(win, app_name, output)
    elif OS == "Linux":
        _screenshot_linux(win, app_name, output)
    elif OS == "Windows":
        _screenshot_windows(win, app_name, output)
    else:
        die(f"Screenshot not supported on {OS}")

def _screenshot_macos(win, app_name: str, output: str):
    window_id = _mac_window_id(win.getHandle())
    if window_id:
        result = subprocess.run(
            ["screencapture", "-l", window_id, "-x", output],
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            ok(f"Screenshot of '{app_name}' (window ID: {window_id}) → {output}")
            return
        info("Window ID capture failed; trying window bounds...")

    region = _window_region(win)
    if not region:
        die(f"Could not determine a screenshot region for '{app_name}'")

    result = subprocess.run(
        ["screencapture", "-R", region, "-x", output],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        detail = (result.stderr or result.stdout).strip()
        if detail:
            die(f"Could not capture '{app_name}' region {region}: {detail}")
        die(f"Could not capture '{app_name}' region {region}")

    ok(f"Screenshot of '{app_name}' (region: {region}) → {output}")

def _mac_window_id(handle):
    if isinstance(handle, bool):
        return None
    if isinstance(handle, int):
        return str(handle)
    if isinstance(handle, str) and handle.isdigit():
        return handle
    if isinstance(handle, (list, tuple)):
        for item in handle:
            window_id = _mac_window_id(item)
            if window_id:
                return window_id
    return None

def _window_region(win):
    left = _window_number(win, "left")
    top = _window_number(win, "top")
    width = _window_number(win, "width")
    height = _window_number(win, "height")

    if width is None or height is None:
        right = _window_number(win, "right")
        bottom = _window_number(win, "bottom")
        if left is not None and right is not None:
            width = right - left
        if top is not None and bottom is not None:
            height = bottom - top

    if None in (left, top, width, height):
        return None
    if width <= 0 or height <= 0:
        return None

    return f"{left},{top},{width},{height}"

def _window_number(win, attr: str):
    try:
        value = getattr(win, attr)
        if callable(value):
            value = value()
    except Exception:
        return None

    try:
        return int(round(float(value)))
    except (TypeError, ValueError):
        return None

def _screenshot_linux(win, app_name: str, output: str):
    wid = win.getHandle()
    if wid and _cmd_exists("import"):
        subprocess.run(["import", "-window", str(wid), output], check=True)
        ok(f"Screenshot of '{app_name}' → {output}")
    elif wid and _cmd_exists("scrot"):
        subprocess.run(["scrot", "--focused", output], check=True)
        ok(f"Screenshot of '{app_name}' → {output}")
    else:
        die("Install imagemagick or scrot: sudo apt install imagemagick scrot")

def _screenshot_windows(win, app_name: str, output: str):
    try:
        from PIL import ImageGrab
        # Grab the bounding box of the window
        box = (win.left, win.top, win.right, win.bottom)
        img = ImageGrab.grab(bbox=box)
        img.save(output)
        ok(f"Screenshot of '{app_name}' → {output}")
    except ImportError:
        # Fallback: PowerShell
        ps = f"""
Add-Type -AssemblyName System.Windows.Forms
$bmp = New-Object System.Drawing.Bitmap({win.width}, {win.height})
$g = [System.Drawing.Graphics]::FromImage($bmp)
$g.CopyFromScreen({win.left}, {win.top}, 0, 0, $bmp.Size)
$bmp.Save('{output}')
"""
        subprocess.run(["powershell", "-Command", ps], check=True)
        ok(f"Screenshot of '{app_name}' → {output}")

def _cmd_exists(cmd: str) -> bool:
    import shutil
    return shutil.which(cmd) is not None

# ─── Usage ────────────────────────────────────────────────────────────────────

def usage():
    print(__doc__)
    sys.exit(0)

# ─── Dispatch ─────────────────────────────────────────────────────────────────

def main():
    if len(sys.argv) < 2:
        usage()

    cmd = sys.argv[1].lower()
    rest = sys.argv[2:]

    if cmd in ("list",):
        cmd_list()
    elif cmd in ("open", "launch", "start"):
        if not rest: die("Usage: winctl.py open <AppName>")
        cmd_open(rest[0])
    elif cmd in ("front", "activate", "focus"):
        if not rest: die("Usage: winctl.py front <AppName>")
        cmd_front(rest[0])
    elif cmd in ("minimize", "min"):
        if not rest: die("Usage: winctl.py minimize <AppName>")
        cmd_minimize(rest[0])
    elif cmd in ("maximize", "max"):
        if not rest: die("Usage: winctl.py maximize <AppName>")
        cmd_maximize(rest[0])
    elif cmd in ("restore",):
        if not rest: die("Usage: winctl.py restore <AppName>")
        cmd_restore(rest[0])
    elif cmd in ("screenshot", "snap", "shot"):
        cmd_screenshot(rest)
    elif cmd in ("help", "--help", "-h"):
        usage()
    else:
        die(f"Unknown command: '{cmd}'. Run 'winctl.py help' for usage.")

if __name__ == "__main__":
    main()
