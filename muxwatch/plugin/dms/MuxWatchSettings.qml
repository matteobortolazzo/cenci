import QtQuick
import qs.Common
import qs.Widgets
import qs.Modules.Plugins

PluginSettings {
    id: root
    pluginId: "muxwatch"

    StyledText {
        width: parent.width
        text: "MuxWatch"
        font.pixelSize: Theme.fontSizeLarge
        font.weight: Font.Bold
        color: Theme.surfaceText
    }

    StyledText {
        width: parent.width
        text: "Polls `muxwatch waybar` and surfaces running Claude Code tmux sessions in the bar."
        font.pixelSize: Theme.fontSizeSmall
        color: Theme.surfaceVariantText
        wrapMode: Text.WordWrap
    }

    SliderSetting {
        settingKey: "pollIntervalMs"
        label: "Poll interval"
        description: "How often to invoke `muxwatch waybar` (in milliseconds)."
        defaultValue: 2000
        minimum: 500
        maximum: 10000
        unit: "ms"
        leftIcon: "speed"
    }

    StringSetting {
        settingKey: "muxwatchPath"
        label: "muxwatch binary"
        description: "Path or command name for the muxwatch binary. Leave as `muxwatch` if it's on $PATH."
        defaultValue: "muxwatch"
        placeholder: "muxwatch"
    }
}
