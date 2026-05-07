import QtQuick
import Quickshell
import Quickshell.Io
import qs.Commons

Item {
  id: root

  property var pluginApi: null

  readonly property int pollIntervalMs: pluginApi?.pluginSettings.pollIntervalMs
    || pluginApi?.manifest?.metadata?.defaultSettings?.pollIntervalMs || 2000
  readonly property string muxwatchPath: pluginApi?.pluginSettings.muxwatchPath
    || pluginApi?.manifest?.metadata?.defaultSettings?.muxwatchPath || "muxwatch"

  property string text: ""
  property string tooltip: ""
  property string cssClass: "none"
  property bool hasOutput: false

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
    command: ["sh", "-c", root.muxwatchPath + " waybar"]

    stdout: StdioCollector {
      onStreamFinished: {
        const out = text.trim()
        if (!out) {
          root.hasOutput = false
          return
        }
        try {
          const j = JSON.parse(out)
          root.text = j.text || ""
          root.tooltip = j.tooltip || ""
          root.cssClass = j["class"] || "none"
          root.hasOutput = root.cssClass !== "none"
        } catch (e) {
          Logger.e("MuxWatch", "parse error:", e, out)
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
}
