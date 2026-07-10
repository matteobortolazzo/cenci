import QtQuick
import Quickshell.Io
import qs.Common
import qs.Widgets
import qs.Modules.Plugins

PluginComponent {
    id: root

    readonly property int pollIntervalMs: {
        const v = parseInt(pluginData?.pollIntervalMs)
        return (!isNaN(v) && v >= 250) ? v : 2000
    }
    readonly property string agentwatchPath: pluginData?.agentwatchPath || "agentwatch"

    property string statusText: ""
    property string tooltipText: ""
    property string cssClass: "none"
    property bool hasOutput: false

    readonly property var tooltipLines: tooltipText.length > 0 ? tooltipText.split("\n").filter(function (l) { return l.length > 0 }) : []
    readonly property int sessionCount: tooltipLines.length

    onHasOutputChanged: setVisibilityOverride(hasOutput)
    Component.onCompleted: setVisibilityOverride(false)

    function iconForClass(c) {
        switch (c) {
        case "failed":     return "error"
        case "need-input": return "priority_high"
        case "running":    return "smart_toy"
        case "done":       return "check_circle"
        case "stopped":    return "pause_circle"
        case "idle":       return "smart_toy"
        default:           return "smart_toy"
        }
    }

    function colorForClass(c) {
        switch (c) {
        case "failed":     return Theme.error
        case "need-input": return Theme.error
        case "running":    return Theme.primary
        case "done":       return Theme.tertiary
        case "stopped":    return Theme.secondary
        case "idle":       return Theme.surfaceVariantText
        default:           return Theme.surfaceText
        }
    }

    function statusOf(line) {
        const m = line.match(/\(([a-z-]+)\)\s*$/)
        return m ? m[1] : "none"
    }

    Timer {
        id: pollTimer
        interval: root.pollIntervalMs
        running: true
        repeat: true
        triggeredOnStart: true
        onTriggered: {
            if (!poll.running) {
                poll.running = true
            }
        }
    }

    Process {
        id: poll
        command: ["sh", "-c", root.agentwatchPath + " waybar"]

        stdout: StdioCollector {
            onStreamFinished: {
                const out = text.trim()
                if (!out) {
                    root.hasOutput = false
                    return
                }
                try {
                    const j = JSON.parse(out)
                    root.statusText = j.text || ""
                    root.tooltipText = j.tooltip || ""
                    root.cssClass = j["class"] || "none"
                    root.hasOutput = root.cssClass !== "none"
                } catch (e) {
                    console.warn("AgentWatch parse error:", e, out)
                    root.hasOutput = false
                }
            }
        }

        onExited: function (code) {
            if (code !== 0) {
                root.hasOutput = false
            }
        }
    }

    horizontalBarPill: Component {
        Row {
            spacing: Theme.spacingXS

            DankIcon {
                anchors.verticalCenter: parent.verticalCenter
                name: root.iconForClass(root.cssClass)
                size: Theme.iconSize - 6
                color: root.colorForClass(root.cssClass)
            }

            StyledText {
                anchors.verticalCenter: parent.verticalCenter
                text: root.statusText
                font.pixelSize: Theme.fontSizeSmall
                font.weight: Font.Medium
                color: Theme.surfaceText
            }
        }
    }

    verticalBarPill: Component {
        Column {
            spacing: 1

            DankIcon {
                anchors.horizontalCenter: parent.horizontalCenter
                name: root.iconForClass(root.cssClass)
                size: Theme.iconSize - 6
                color: root.colorForClass(root.cssClass)
            }

            StyledText {
                anchors.horizontalCenter: parent.horizontalCenter
                text: root.statusText
                font.pixelSize: Theme.fontSizeSmall
                font.weight: Font.Medium
                color: Theme.surfaceText
            }
        }
    }

    popoutWidth: 380
    popoutHeight: 320

    popoutContent: Component {
        PopoutComponent {
            id: popout
            headerText: "AgentWatch"
            detailsText: root.sessionCount === 1
                ? "1 session"
                : root.sessionCount + " sessions"
            showCloseButton: true

            Column {
                width: parent.width
                spacing: Theme.spacingXS

                Repeater {
                    model: root.tooltipLines

                    delegate: Item {
                        required property string modelData
                        readonly property string status: root.statusOf(modelData)
                        readonly property string body: modelData.replace(/\s*\([a-z-]+\)\s*$/, "")

                        width: parent.width
                        height: rowLayout.implicitHeight + Theme.spacingXS * 2

                        Rectangle {
                            anchors.fill: parent
                            radius: Theme.cornerRadius
                            color: Theme.surfaceContainer
                            opacity: 0.6
                        }

                        Row {
                            id: rowLayout
                            anchors.left: parent.left
                            anchors.right: parent.right
                            anchors.verticalCenter: parent.verticalCenter
                            anchors.leftMargin: Theme.spacingS
                            anchors.rightMargin: Theme.spacingS
                            spacing: Theme.spacingS

                            DankIcon {
                                anchors.verticalCenter: parent.verticalCenter
                                name: root.iconForClass(status)
                                size: Theme.iconSize - 6
                                color: root.colorForClass(status)
                            }

                            StyledText {
                                anchors.verticalCenter: parent.verticalCenter
                                text: body
                                font.pixelSize: Theme.fontSizeSmall
                                color: Theme.surfaceText
                                width: rowLayout.width - Theme.iconSize - statusBadge.width - Theme.spacingS * 3
                                elide: Text.ElideRight
                            }

                            StyledText {
                                id: statusBadge
                                anchors.verticalCenter: parent.verticalCenter
                                text: status
                                font.pixelSize: Theme.fontSizeSmall
                                font.weight: Font.Medium
                                color: root.colorForClass(status)
                            }
                        }
                    }
                }

                StyledText {
                    width: parent.width
                    visible: root.sessionCount === 0
                    text: "No active sessions."
                    font.pixelSize: Theme.fontSizeSmall
                    color: Theme.surfaceVariantText
                    horizontalAlignment: Text.AlignHCenter
                    topPadding: Theme.spacingL
                }
            }
        }
    }
}
