import QtQuick
import qs.Common
import qs.Widgets
import qs.Modules.Plugins

PluginSettings {
    id: root
    pluginId: "cenci"

    StyledText {
        width: parent.width
        text: "Cenci"
        font.pixelSize: Theme.fontSizeLarge
        font.weight: Font.Bold
        color: Theme.surfaceText
    }

    StyledText {
        width: parent.width
        text: "Polls `cenci waybar` and surfaces running Claude Code tmux sessions in the bar."
        font.pixelSize: Theme.fontSizeSmall
        color: Theme.surfaceVariantText
        wrapMode: Text.WordWrap
    }

    SliderSetting {
        settingKey: "pollIntervalMs"
        label: "Poll interval"
        description: "How often to invoke `cenci waybar` (in milliseconds)."
        defaultValue: 2000
        minimum: 500
        maximum: 10000
        unit: "ms"
        leftIcon: "speed"
    }

    StringSetting {
        settingKey: "cenciPath"
        label: "cenci binary"
        description: "Path or command name for the cenci binary. Leave as `cenci` if it's on $PATH."
        defaultValue: "cenci"
        placeholder: "cenci"
    }
}
