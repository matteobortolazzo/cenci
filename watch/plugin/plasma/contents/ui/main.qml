// AgentWatch — KDE Plasma 6 plasmoid.
//
// Live Claude Code / Codex session counts in the Plasma panel. Read-only
// frontend over the same Waybar JSON contract consumed by the waybar, noctalia,
// dms, and macOS widgets — no daemon or Go changes. Polls `agentwatch widget-json`
// on a timer, parses the single JSON line, and maps the `class` field to an icon
// + color. Empty stdout, non-zero exit, or `alt: "none"` → hide from the panel
// (`alt`, not `class`, is the hide/show contract — `class` stays "none" for the
// zero-sessions-but-dispatch-enabled case, `alt: "dispatch-only"`).

import QtQuick
import QtQuick.Layouts
import org.kde.plasma.plasmoid
import org.kde.plasma.core as PlasmaCore
import org.kde.plasma.components as PlasmaComponents
import org.kde.plasma.plasma5support as Plasma5Support
import org.kde.kirigami as Kirigami

PlasmoidItem {
    id: root

    property string statusText: ""
    property string tooltipText: ""
    property string cssClass: "none"
    property string cssAlt: "none"
    property bool hasOutput: false
    property var headroom: ({})

    readonly property int pollIntervalMs: {
        const v = parseInt(Plasmoid.configuration.pollIntervalMs)
        return (!isNaN(v) && v >= 250) ? v : 2000
    }
    readonly property string agentwatchPath: Plasmoid.configuration.agentwatchPath || "cenci"

    // Sorted agent keys with budget headroom data — deterministic order,
    // mirrors formatHeadroom in status.go / hKeys.sort() in the JXA script.
    readonly property var headroomKeys: Object.keys(headroom).sort()

    // headroomPercent converts a 0.0-1.0 headroom fraction to an integer
    // percent, rounding half up. Mirrors headroomPercent in status.go.
    function headroomPercent(frac) {
        return Math.floor(frac * 100 + 0.5)
    }

    // The compact headroom summary line status.go appends to the tooltip
    // (e.g. "claude 73%  codex 15%"), recomputed here so it can be recognized
    // and excluded from tooltipLines below. Empty when there's no headroom
    // data — never matches a real tooltip line, since tips are pre-filtered
    // to non-empty lines.
    readonly property string headroomSummaryLine: headroomKeys.map(function (a) {
        return a + " " + headroomPercent(headroom[a]) + "%"
    }).join("  ")

    // Lines ending in "(status)" are session rows. The known headroom summary
    // line (computed above) is skipped by EXACT match only — per the narrow-skip
    // lesson, any other non-matching line still falls through to the visible
    // 'none' status fallback rather than being silently dropped.
    readonly property var tooltipLines: {
        if (tooltipText.length === 0) return []
        const summary = headroomSummaryLine
        return tooltipText.split("\n").filter(function (l) {
            return l.length > 0 && !(summary.length > 0 && l === summary)
        })
    }
    readonly property int sessionCount: tooltipLines.length

    // Drop out of the panel entirely when there is nothing live.
    Plasmoid.status: hasOutput ? PlasmaCore.Types.ActiveStatus : PlasmaCore.Types.HiddenStatus

    // class -> panel icon (Breeze names). Priority mirrors the formatter:
    // failed > need-input > running > done > stopped > idle.
    function iconForClass(c) {
        switch (c) {
        case "failed":     return "dialog-error"
        case "need-input": return "dialog-warning"
        case "running":    return "state-sync"
        case "done":       return "dialog-ok"
        case "stopped":    return "media-playback-pause"
        case "idle":       return "face-smile"
        default:           return "face-smile"
        }
    }

    function colorForClass(c) {
        switch (c) {
        case "failed":     return Kirigami.Theme.negativeTextColor
        case "need-input": return Kirigami.Theme.negativeTextColor
        case "running":    return Kirigami.Theme.highlightColor
        case "done":       return Kirigami.Theme.positiveTextColor
        case "stopped":    return Kirigami.Theme.neutralTextColor
        case "idle":       return Kirigami.Theme.disabledTextColor
        default:           return Kirigami.Theme.textColor
        }
    }

    // headroomClassFor returns the threshold class for a headroom percentage:
    // >25% normal, 10-25% (inclusive) warning, <10% critical. Mirrors
    // headroomClass in status.go.
    function headroomClassFor(pct) {
        if (pct > 25) return "normal"
        if (pct >= 10) return "warning"
        return "critical"
    }

    function colorForHeadroomClass(c) {
        switch (c) {
        case "normal":   return Kirigami.Theme.positiveTextColor
        case "warning":  return Kirigami.Theme.neutralTextColor
        case "critical": return Kirigami.Theme.negativeTextColor
        default:         return Kirigami.Theme.textColor
        }
    }

    // Each tooltip line ends with its own "(status)".
    function statusOf(line) {
        const m = line.match(/\(([a-z-]+)\)\s*$/)
        return m ? m[1] : "none"
    }

    Plasma5Support.DataSource {
        id: pollSource
        engine: "executable"
        connectedSources: []

        onNewData: function (sourceName, data) {
            disconnectSource(sourceName)
            const out = (data["stdout"] || "").trim()
            const exitCode = data["exit code"]
            // Non-zero exit / empty output = daemon down or no sessions.
            if (exitCode !== 0 || !out) {
                root.hasOutput = false
                return
            }
            try {
                const j = JSON.parse(out)
                root.statusText = j.text || ""
                root.tooltipText = j.tooltip || ""
                root.cssClass = j["class"] || "none"
                root.cssAlt = j["alt"] || "none"
                root.headroom = j.headroom || ({})
                root.hasOutput = root.cssAlt !== "none"
            } catch (e) {
                console.warn("AgentWatch parse error:", e, out)
                root.hasOutput = false
            }
        }

        function poll() {
            connectSource(root.agentwatchPath + " widget-json")
        }
    }

    Timer {
        interval: root.pollIntervalMs
        running: true
        repeat: true
        triggeredOnStart: true
        onTriggered: pollSource.poll()
    }

    compactRepresentation: MouseArea {
        onClicked: root.expanded = !root.expanded

        Row {
            anchors.centerIn: parent
            spacing: Kirigami.Units.smallSpacing

            Kirigami.Icon {
                anchors.verticalCenter: parent.verticalCenter
                source: root.iconForClass(root.cssClass)
                color: root.colorForClass(root.cssClass)
                width: Kirigami.Units.iconSizes.small
                height: width
            }

            PlasmaComponents.Label {
                anchors.verticalCenter: parent.verticalCenter
                text: root.statusText
            }
        }
    }

    fullRepresentation: Item {
        Layout.minimumWidth: Kirigami.Units.gridUnit * 16
        Layout.minimumHeight: Kirigami.Units.gridUnit * 12
        Layout.preferredWidth: Kirigami.Units.gridUnit * 20
        Layout.preferredHeight: Kirigami.Units.gridUnit * 16

        ColumnLayout {
            anchors.fill: parent
            anchors.margins: Kirigami.Units.smallSpacing
            spacing: Kirigami.Units.smallSpacing

            Kirigami.Heading {
                level: 3
                text: root.sessionCount === 1
                    ? i18n("1 session")
                    : i18n("%1 sessions", root.sessionCount)
            }

            ListView {
                Layout.fillWidth: true
                Layout.fillHeight: true
                clip: true
                spacing: Kirigami.Units.smallSpacing
                model: root.tooltipLines

                delegate: RowLayout {
                    required property string modelData
                    readonly property string st: root.statusOf(modelData)
                    readonly property string body: modelData.replace(/\s*\([a-z-]+\)\s*$/, "")

                    width: ListView.view.width
                    spacing: Kirigami.Units.smallSpacing

                    Kirigami.Icon {
                        source: root.iconForClass(st)
                        color: root.colorForClass(st)
                        Layout.preferredWidth: Kirigami.Units.iconSizes.small
                        Layout.preferredHeight: Kirigami.Units.iconSizes.small
                    }

                    PlasmaComponents.Label {
                        Layout.fillWidth: true
                        text: body
                        elide: Text.ElideRight
                    }

                    PlasmaComponents.Label {
                        text: st
                        color: root.colorForClass(st)
                        font: Kirigami.Theme.smallFont
                    }
                }
            }

            PlasmaComponents.Label {
                Layout.fillWidth: true
                visible: root.sessionCount === 0
                text: i18n("No active sessions.")
                horizontalAlignment: Text.AlignHCenter
                opacity: 0.7
            }

            ColumnLayout {
                Layout.fillWidth: true
                visible: root.headroomKeys.length > 0
                spacing: Kirigami.Units.smallSpacing

                Kirigami.Heading {
                    level: 5
                    text: i18n("Budget headroom")
                }

                Repeater {
                    model: root.headroomKeys

                    delegate: RowLayout {
                        required property string modelData
                        // A non-numeric fraction must fail visibly ("n/a"), not
                        // coerce NaN -> 0 (QML int semantics) and masquerade as
                        // a genuinely exhausted budget.
                        readonly property bool validFrac: typeof root.headroom[modelData] === "number"
                                                          && isFinite(root.headroom[modelData])
                        readonly property int pct: validFrac ? root.headroomPercent(root.headroom[modelData]) : 0

                        Layout.fillWidth: true

                        PlasmaComponents.Label {
                            Layout.fillWidth: true
                            textFormat: Text.PlainText
                            text: validFrac ? modelData + " " + pct + "%" : modelData + " n/a"
                            color: validFrac ? root.colorForHeadroomClass(root.headroomClassFor(pct))
                                             : Kirigami.Theme.textColor
                        }
                    }
                }
            }
        }
    }
}
