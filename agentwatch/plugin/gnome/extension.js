// AgentWatch — GNOME Shell extension (GNOME 45+, ESM).
//
// Live Claude Code / Codex session counts in the top bar. This is a read-only
// frontend over the same Waybar JSON contract consumed by the waybar, noctalia,
// dms, and macOS widgets — it makes no daemon or Go changes. It polls
// `agentwatch status` on a timer, parses the single JSON line, and maps the
// `class` field to an icon + color. Empty stdout, a non-zero exit, or
// class "none" all mean "no live sessions" → hide the indicator.

import St from 'gi://St';
import GLib from 'gi://GLib';
import Gio from 'gi://Gio';
import GObject from 'gi://GObject';
import Clutter from 'gi://Clutter';

import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';
import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import * as PanelMenu from 'resource:///org/gnome/shell/ui/panelMenu.js';
import * as PopupMenu from 'resource:///org/gnome/shell/ui/popupMenu.js';

// class -> panel icon (symbolic, follows the active icon theme). Priority order
// mirrors the formatter: failed > need-input > running > done > stopped > idle.
const ICON_FOR_CLASS = {
    'failed': 'dialog-error-symbolic',
    'need-input': 'dialog-warning-symbolic',
    'running': 'emblem-synchronizing-symbolic',
    'done': 'emblem-ok-symbolic',
    'stopped': 'media-playback-pause-symbolic',
    'idle': 'face-smile-symbolic',
};

function iconForClass(c) {
    return ICON_FOR_CLASS[c] || 'face-smile-symbolic';
}

// Color lives in stylesheet.css as `.agentwatch-<class>`; GNOME auto-loads it.
function styleClassForClass(c) {
    return `agentwatch-icon agentwatch-${c || 'idle'}`;
}

// Each tooltip line ends with its own "(status)" — extract it so a need-input
// row stays loud even when the summary class is merely running.
function statusOf(line) {
    const m = line.match(/\(([a-z-]+)\)\s*$/);
    return m ? m[1] : 'none';
}

// headroomPercent converts a 0.0-1.0 headroom fraction to an integer percent,
// rounding half up. Mirrors headroomPercent in status.go.
function headroomPercent(frac) {
    return Math.floor(frac * 100 + 0.5);
}

// headroomClass returns the threshold class for a headroom percentage:
// >25% normal, 10-25% (inclusive) warning, <10% critical. Verbatim port of
// headroomClass in status.go / colorForHeadroom in agentwatch.5s.sh.
function headroomClass(pct) {
    if (pct > 25)
        return 'normal';
    if (pct >= 10)
        return 'warning';
    return 'critical';
}

const AgentWatchIndicator = GObject.registerClass(
class AgentWatchIndicator extends PanelMenu.Button {
    _init() {
        super._init(0.0, 'AgentWatch');

        this._box = new St.BoxLayout({style_class: 'panel-status-menu-box'});
        this._icon = new St.Icon({
            icon_name: iconForClass('idle'),
            style_class: styleClassForClass('idle'),
            icon_size: 16,
            y_align: Clutter.ActorAlign.CENTER,
        });
        this._label = new St.Label({
            text: '',
            y_align: Clutter.ActorAlign.CENTER,
        });
        this._box.add_child(this._icon);
        this._box.add_child(this._label);
        this.add_child(this._box);
    }

    // updateFromJSON consumes one line of `agentwatch status` output.
    updateFromJSON(stdout) {
        const out = (stdout || '').trim();
        if (!out) {
            this.setEmpty();
            return;
        }
        let j;
        try {
            j = JSON.parse(out);
        } catch (_e) {
            this.setEmpty();
            return;
        }
        const cls = j['class'] || 'none';
        if (cls === 'none') {
            this.setEmpty();
            return;
        }
        const lines = (j.tooltip || '').split('\n').filter(l => l.length > 0);
        const headroom = j.headroom || {};
        this._render(j.text || '', lines, cls, headroom);
    }

    // setEmpty hides the indicator (no live sessions / daemon down).
    setEmpty() {
        this.visible = false;
        this.menu.close();
    }

    _render(text, lines, cls, headroom) {
        this._icon.icon_name = iconForClass(cls);
        this._icon.style_class = styleClassForClass(cls);
        this._label.text = text;
        this._rebuildMenu(lines, headroom);
        this.visible = true;
    }

    _rebuildMenu(lines, headroom) {
        this.menu.removeAll();
        if (lines.length === 0) {
            const item = new PopupMenu.PopupMenuItem('No active sessions', {reactive: false});
            this.menu.addMenuItem(item);
            return;
        }

        // Agent keys with budget headroom data, sorted for deterministic
        // order. Also used to compute the exact compact headroom summary
        // line status.go appends to tooltip (e.g. "claude 73%  codex 15%"),
        // mirroring formatHeadroom in status.go, so that specific line can
        // be recognized and skipped when parsing session rows below (it's
        // rendered separately from the numeric `headroom` field instead).
        // Empty string when there's no headroom data — never matches a real
        // tooltip line, since tips are pre-filtered to non-empty lines.
        const hKeys = Object.keys(headroom).sort();
        const headroomLine = hKeys.map(a => `${a} ${headroomPercent(headroom[a])}%`).join('  ');

        for (const line of lines) {
            // The known headroom summary line (computed above) is skipped
            // by exact match. Any OTHER non-matching line still renders with
            // the 'none' status fallback instead of being silently dropped,
            // so a real regression stays visible rather than vanishing.
            if (headroomLine !== '' && line === headroomLine)
                continue;
            const status = statusOf(line);
            const body = line.replace(/\s*\([a-z-]+\)\s*$/, '');
            const item = new PopupMenu.PopupImageMenuItem(
                body, iconForClass(status), {reactive: false});
            item._icon.add_style_class_name(`agentwatch-${status}`);
            this.menu.addMenuItem(item);
        }

        // One dropdown row per agent with budget headroom data. Omitted
        // entirely when the field is absent/empty — no headroom leaked into
        // the menu.
        for (const agent of hKeys) {
            const pct = headroomPercent(headroom[agent]);
            const item = new PopupMenu.PopupMenuItem(
                `${agent} ${pct}%`, {reactive: false});
            item.label.add_style_class_name(`agentwatch-headroom-${headroomClass(pct)}`);
            this.menu.addMenuItem(item);
        }
    }
});

export default class AgentWatchExtension extends Extension {
    enable() {
        this._settings = this.getSettings();
        this._cancellable = new Gio.Cancellable();

        this._indicator = new AgentWatchIndicator();
        this._indicator.visible = false;
        Main.panel.addToStatusArea(this.uuid, this._indicator);

        // Re-arm the timer whenever the interval changes.
        this._settingsChangedId = this._settings.connect(
            'changed::poll-interval-ms', () => this._restartTimer());

        this._startTimer();
        this._poll(); // first sample immediately, don't wait a full interval
    }

    disable() {
        if (this._timeoutId) {
            GLib.Source.remove(this._timeoutId);
            this._timeoutId = null;
        }
        if (this._settingsChangedId) {
            this._settings.disconnect(this._settingsChangedId);
            this._settingsChangedId = null;
        }
        if (this._cancellable) {
            this._cancellable.cancel();
            this._cancellable = null;
        }
        if (this._indicator) {
            this._indicator.destroy();
            this._indicator = null;
        }
        this._settings = null;
    }

    _interval() {
        const v = this._settings.get_int('poll-interval-ms');
        return v >= 250 ? v : 2000;
    }

    _startTimer() {
        this._timeoutId = GLib.timeout_add(
            GLib.PRIORITY_DEFAULT, this._interval(), () => {
                this._poll();
                return GLib.SOURCE_CONTINUE;
            });
    }

    _restartTimer() {
        if (this._timeoutId) {
            GLib.Source.remove(this._timeoutId);
            this._timeoutId = null;
        }
        this._startTimer();
    }

    _poll() {
        const path = this._settings.get_string('agentwatch-path') || 'agentwatch';
        let proc;
        try {
            proc = Gio.Subprocess.new(
                [path, 'status'],
                Gio.SubprocessFlags.STDOUT_PIPE | Gio.SubprocessFlags.STDERR_SILENCE);
        } catch (_e) {
            // Binary not found / not executable — hide.
            this._indicator?.setEmpty();
            return;
        }
        proc.communicate_utf8_async(null, this._cancellable, (p, res) => {
            let stdout = '';
            let ok = false;
            try {
                [, stdout] = p.communicate_utf8_finish(res);
                ok = p.get_successful();
            } catch (_e) {
                // Cancelled (disable) or spawn failure — leave the indicator alone.
                return;
            }
            if (!this._indicator)
                return;
            // Non-zero exit is the daemon-down / no-sessions signal.
            if (!ok)
                this._indicator.setEmpty();
            else
                this._indicator.updateFromJSON(stdout);
        });
    }
}
