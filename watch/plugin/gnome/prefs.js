// AgentWatch — GNOME Shell extension preferences (GNOME 45+, libadwaita).

import Adw from 'gi://Adw';
import Gtk from 'gi://Gtk';
import Gio from 'gi://Gio';

import {ExtensionPreferences} from 'resource:///org/gnome/Shell/Extensions/js/extensions/prefs.js';

export default class AgentWatchPrefs extends ExtensionPreferences {
    fillPreferencesWindow(window) {
        const settings = this.getSettings();

        const page = new Adw.PreferencesPage();
        const group = new Adw.PreferencesGroup({
            title: 'AgentWatch',
            description: 'How the top-bar indicator polls the cenci daemon.',
        });
        page.add(group);

        // Binary path — use an absolute path if cenci is not on the GNOME
        // session PATH (the shell's PATH under Wayland is often minimal).
        const pathRow = new Adw.EntryRow({title: 'cenci binary path'});
        settings.bind('agentwatch-path', pathRow, 'text', Gio.SettingsBindFlags.DEFAULT);
        group.add(pathRow);

        const intervalRow = new Adw.SpinRow({
            title: 'Poll interval (ms)',
            adjustment: new Gtk.Adjustment({
                lower: 250,
                upper: 60000,
                step_increment: 250,
                page_increment: 1000,
            }),
        });
        settings.bind('poll-interval-ms', intervalRow, 'value', Gio.SettingsBindFlags.DEFAULT);
        group.add(intervalRow);

        window.add(page);
    }
}
