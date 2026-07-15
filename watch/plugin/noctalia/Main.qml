import QtQuick
import Quickshell
import Quickshell.Io
import qs.Commons

Item {
  id: root

  property var pluginApi: null

  readonly property int pollIntervalMs: pluginApi?.pluginSettings.pollIntervalMs
    || pluginApi?.manifest?.metadata?.defaultSettings?.pollIntervalMs || 2000
  readonly property string cenciPath: pluginApi?.pluginSettings.cenciPath
    || pluginApi?.manifest?.metadata?.defaultSettings?.cenciPath || "cenci"

  property string text: ""
  property string tooltip: ""
  property string cssClass: "none"
  property string cssAlt: "none"
  property bool hasOutput: false
  property var headroom: ({})

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
    command: ["sh", "-c", root.cenciPath + " waybar"]

    stdout: StdioCollector {
      onStreamFinished: {
        const out = text.trim()
        if (!out) {
          root.hasOutput = false
          root.headroom = {}
          return
        }
        try {
          const j = JSON.parse(out)
          root.text = j.text || ""
          root.tooltip = j.tooltip || ""
          root.cssClass = j["class"] || "none"
          root.cssAlt = j["alt"] || "none"
          root.hasOutput = root.cssAlt !== "none"
          root.headroom = j.headroom || {}
        } catch (e) {
          Logger.e("Cenci", "parse error:", e, out)
          root.hasOutput = false
          root.headroom = {}
        }
      }
    }

    onExited: function (code) {
      if (code !== 0) {
        root.hasOutput = false
        root.headroom = {}
      }
    }
  }
}
