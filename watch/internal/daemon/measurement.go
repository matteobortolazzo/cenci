package daemon

import (
	"log"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/measurement"
)

// applyNativeMeasurement validates a native Claude measurement envelope and
// stores it on the tracked session it names (#1450). The event socket also
// receives writes from containers, so the body is re-validated here with the
// same measurement.Parse that cenci notify uses.
//
// The sample is display-only and lifecycle-neutral: it never creates a session,
// never changes Status, LastEvent or BackgroundHold, and never reaches the
// frontend, so a session that only receives measurements still expires at the
// TTL. Every drop is logged under cfg.Verbose.
func (d *Daemon) applyNativeMeasurement(nm ipc.NativeMeasurement) {
	sample, err := measurement.Parse(nm.Measurement, d.now())
	if err != nil {
		d.logMeasurementDrop("rejected: %v", err)
		return
	}
	sess, ok := d.sessions[sample.SessionID]
	if !ok {
		d.logMeasurementDrop("session %s is not tracked", sample.SessionID)
		return
	}
	if sess.NativeMeasurement != nil && !sample.MeasuredAt.After(sess.NativeMeasuredAt) {
		d.logMeasurementDrop("session %s: measured_at %s is not newer than the stored sample", sample.SessionID, sample.Snapshot.MeasuredAt)
		return
	}

	snap := sample.Snapshot
	sess.NativeMeasurement = &snap
	sess.NativeMeasuredAt = sample.MeasuredAt
	if d.cfg.Verbose {
		log.Printf("native-measurement: stored on session %s (%d missing, %d invalid), status unchanged", sample.SessionID, len(snap.Missing), len(snap.Invalid))
	}
	d.broadcast()
}

// logMeasurementDrop logs a dropped measurement under cfg.Verbose. Callers pass
// only parsed or bounded values, never raw producer bytes.
func (d *Daemon) logMeasurementDrop(format string, args ...any) {
	if d.cfg.Verbose {
		log.Printf("native-measurement: dropped, "+format, args...)
	}
}
