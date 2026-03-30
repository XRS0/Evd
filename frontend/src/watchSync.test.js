import test from 'node:test'
import assert from 'node:assert/strict'

import {
  ROOM_STATUS_PAUSED,
  ROOM_STATUS_PLAYING,
  chooseDriftCorrection,
  computeTargetPosition,
  createClockEstimate,
  normalizePlaybackState,
  shouldAcceptPlaybackState,
  updateClockEstimate
} from './watchSync.js'

test('normalizePlaybackState maps hub snapshot into canonical fields', () => {
  const playback = normalizePlaybackState({
    id: 'hub-1',
    videoPath: 'movie.mp4',
    basePositionSec: 12,
    playbackRate: 1.25,
    stateVersion: 3,
    serverTimestampMs: 1000,
    status: ROOM_STATUS_PLAYING
  })

  assert.equal(playback.roomId, 'hub-1')
  assert.equal(playback.basePositionSec, 12)
  assert.equal(playback.playbackRate, 1.25)
  assert.equal(playback.stateVersion, 3)
})

test('computeTargetPosition advances only while room is playing', () => {
  assert.equal(computeTargetPosition({
    roomId: 'hub-1',
    videoPath: 'movie.mp4',
    status: ROOM_STATUS_PLAYING,
    basePositionSec: 10,
    playbackRate: 1.5,
    stateVersion: 2,
    serverTimestampMs: 1000
  }, 5000), 16)

  assert.equal(computeTargetPosition({
    roomId: 'hub-1',
    videoPath: 'movie.mp4',
    status: ROOM_STATUS_PAUSED,
    basePositionSec: 10,
    playbackRate: 1.5,
    stateVersion: 2,
    serverTimestampMs: 1000
  }, 5000), 10)
})

test('shouldAcceptPlaybackState rejects stale versions and duplicates', () => {
  assert.equal(shouldAcceptPlaybackState(4, { stateVersion: 4 }), false)
  assert.equal(shouldAcceptPlaybackState(4, { stateVersion: 3 }), false)
  assert.equal(shouldAcceptPlaybackState(4, { stateVersion: 5 }), true)
})

test('chooseDriftCorrection selects ignore, soft, and hard modes', () => {
  assert.deepEqual(chooseDriftCorrection({
    actualPositionSec: 10,
    targetPositionSec: 10.03,
    roomPlaybackRate: 1
  }).mode, 'ignore')

  const soft = chooseDriftCorrection({
    actualPositionSec: 10,
    targetPositionSec: 10.2,
    roomPlaybackRate: 1
  })
  assert.equal(soft.mode, 'soft')
  assert.ok(soft.desiredPlaybackRate > 1)

  const hard = chooseDriftCorrection({
    actualPositionSec: 10,
    targetPositionSec: 10.8,
    roomPlaybackRate: 1
  })
  assert.equal(hard.mode, 'hard')
})

test('updateClockEstimate computes offset from ping/pong', () => {
  const initial = createClockEstimate()
  const next = updateClockEstimate(initial, {
    clientSentMs: 1000,
    clientReceivedMs: 1100,
    serverTimestampMs: 2000
  })

  assert.equal(next.roundTripMs, 100)
  assert.equal(next.offsetMs, 950)
  assert.equal(next.sampleCount, 1)
})
