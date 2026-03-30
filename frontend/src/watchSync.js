export const ROOM_STATUS_PLAYING = 'playing'
export const ROOM_STATUS_PAUSED = 'paused'
export const ROOM_STATUS_BUFFERING = 'buffering'
export const ROOM_STATUS_ENDED = 'ended'

export const SYNC_THRESHOLDS = {
  ignoreDriftSec: 0.08,
  hardCorrectionSec: 0.4,
  maxPlaybackRateAdjustment: 0.12,
  minPlaybackRate: 0.85,
  maxPlaybackRate: 1.15
}

export const createClockEstimate = () => ({
  offsetMs: 0,
  roundTripMs: Number.POSITIVE_INFINITY,
  sampleCount: 0,
  lastSyncedAtMs: 0
})

export const normalizePlaybackState = (hub) => {
  const playback = hub?.playback || hub || {}
  const status = String(playback.status || (hub?.playing ? ROOM_STATUS_PLAYING : ROOM_STATUS_PAUSED) || ROOM_STATUS_PAUSED)
  const playbackRate = Number.isFinite(playback.playbackRate)
    ? playback.playbackRate
    : Number.isFinite(hub?.playbackRate)
      ? hub.playbackRate
      : 1

  return {
    roomId: playback.roomId || hub?.id || '',
    videoPath: playback.videoPath || hub?.videoPath || '',
    status,
    basePositionSec: sanitizeTime(playback.basePositionSec ?? hub?.basePositionSec ?? hub?.currentTime ?? 0),
    playbackRate: sanitizePlaybackRate(playbackRate),
    stateVersion: Number.isFinite(playback.stateVersion)
      ? playback.stateVersion
      : Number.isFinite(hub?.stateVersion)
        ? hub.stateVersion
        : 0,
    serverTimestampMs: Number.isFinite(playback.serverTimestampMs)
      ? playback.serverTimestampMs
      : Number.isFinite(hub?.serverTimestampMs)
        ? hub.serverTimestampMs
        : 0,
    controllerUserId: playback.controllerUserId || hub?.controllerUserId || null
  }
}

export const shouldAcceptPlaybackState = (currentVersion, nextState) => {
  if (!nextState) return false
  return nextState.stateVersion > (Number.isFinite(currentVersion) ? currentVersion : 0)
}

export const estimateServerNow = (clockEstimate, nowMs = Date.now()) => {
  if (!clockEstimate || !Number.isFinite(clockEstimate.offsetMs)) return nowMs
  return nowMs + clockEstimate.offsetMs
}

export const updateClockEstimate = (previousEstimate, sample) => {
  const sentAt = Number(sample?.clientSentMs)
  const receivedAt = Number(sample?.clientReceivedMs)
  const serverTimestampMs = Number(sample?.serverTimestampMs)

  if (!Number.isFinite(sentAt) || !Number.isFinite(receivedAt) || !Number.isFinite(serverTimestampMs)) {
    return previousEstimate || createClockEstimate()
  }

  const rttMs = Math.max(0, receivedAt - sentAt)
  const sampleOffsetMs = serverTimestampMs - (sentAt + rttMs / 2)
  const prev = previousEstimate || createClockEstimate()
  if (prev.sampleCount === 0) {
    return {
      offsetMs: sampleOffsetMs,
      roundTripMs: rttMs,
      sampleCount: 1,
      lastSyncedAtMs: receivedAt
    }
  }

  const preferSample = rttMs <= prev.roundTripMs + 40
  const weight = preferSample ? 0.25 : 0.1
  return {
    offsetMs: prev.offsetMs + (sampleOffsetMs - prev.offsetMs) * weight,
    roundTripMs: Math.min(prev.roundTripMs, rttMs),
    sampleCount: prev.sampleCount + 1,
    lastSyncedAtMs: receivedAt
  }
}

export const computeTargetPosition = (playbackState, estimatedServerNowMs) => {
  const normalized = normalizePlaybackState(playbackState)
  if (normalized.status !== ROOM_STATUS_PLAYING) {
    return normalized.basePositionSec
  }

  const elapsedMs = Math.max(0, estimatedServerNowMs - normalized.serverTimestampMs)
  return sanitizeTime(normalized.basePositionSec + (elapsedMs / 1000) * normalized.playbackRate)
}

export const chooseDriftCorrection = ({
  actualPositionSec,
  targetPositionSec,
  roomPlaybackRate = 1,
  thresholds = SYNC_THRESHOLDS
}) => {
  const driftSec = targetPositionSec - actualPositionSec
  const driftAbs = Math.abs(driftSec)
  const baseRate = sanitizePlaybackRate(roomPlaybackRate)

  if (driftAbs < thresholds.ignoreDriftSec) {
    return {
      mode: 'ignore',
      driftSec,
      desiredPlaybackRate: baseRate
    }
  }

  if (driftAbs > thresholds.hardCorrectionSec) {
    return {
      mode: 'hard',
      driftSec,
      desiredPlaybackRate: baseRate
    }
  }

  const rateDelta = Math.min(
    thresholds.maxPlaybackRateAdjustment,
    Math.max(0.04, driftAbs * 0.3)
  )
  const desiredPlaybackRate = clamp(
    baseRate + Math.sign(driftSec) * rateDelta,
    thresholds.minPlaybackRate,
    thresholds.maxPlaybackRate
  )

  return {
    mode: 'soft',
    driftSec,
    desiredPlaybackRate
  }
}

export function sanitizeTime(value) {
  return Number.isFinite(value) && value >= 0 ? value : 0
}

export function sanitizePlaybackRate(value) {
  return Number.isFinite(value) && value > 0 ? value : 1
}

function clamp(value, min, max) {
  return Math.min(Math.max(value, min), max)
}
