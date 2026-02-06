import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { NavLink, Outlet, Route, Routes, useLocation, useNavigate, useOutletContext, useParams } from 'react-router-dom'
import brandImage from '../images/img.png'
import { getOrCreateDeviceId, loadHistoryForDevice, mergeHistoryEntry, saveHistoryForDevice } from './watchHistory'

const VIDEO_EXTS = ['mp4', 'mkv', 'avi', 'mov']
const HISTORY_FLUSH_INTERVAL_MS = 2000
const RESUME_GUARD_SECONDS = 1

const ROUTE_META = [
  {
    id: 'overview',
    label: 'Overview',
    to: '/',
    title: 'Overview'
  },
  {
    id: 'library',
    label: 'Library',
    to: '/library',
    title: 'Library'
  },
  {
    id: 'torrents',
    label: 'Torrents',
    to: '/torrents',
    title: 'Torrents'
  },
  {
    id: 'player',
    label: 'Player',
    to: '/player',
    title: 'Player'
  },
  {
    id: 'settings',
    label: 'Settings',
    to: '/settings',
    title: 'Settings'
  }
]

const cx = (...parts) => parts.filter(Boolean).join(' ')

const formatBytes = (bytes = 0) => {
  if (!bytes) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / Math.pow(1024, exponent)
  return `${value.toFixed(value >= 10 || exponent === 0 ? 0 : 1)} ${units[exponent]}`
}

const formatDate = (unixSeconds) => {
  if (!unixSeconds) return '—'
  return new Date(unixSeconds * 1000).toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric'
  })
}

const formatDateTime = (unixMs) => {
  if (!unixMs) return '—'
  return new Date(unixMs).toLocaleString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit'
  })
}

const formatEta = (seconds) => {
  if (!seconds || seconds < 0) return '—'
  const mins = Math.floor(seconds / 60)
  const hours = Math.floor(mins / 60)
  const remainingMins = mins % 60
  if (hours > 0) return `${hours}h ${remainingMins}m`
  return `${remainingMins}m`
}

const formatTime = (seconds = 0) => {
  if (!Number.isFinite(seconds) || seconds < 0) return '00:00'
  const total = Math.floor(seconds)
  const hours = Math.floor(total / 3600)
  const mins = Math.floor((total % 3600) / 60)
  const secs = total % 60
  if (hours > 0) {
    return `${String(hours).padStart(2, '0')}:${String(mins).padStart(2, '0')}:${String(secs).padStart(2, '0')}`
  }
  return `${String(mins).padStart(2, '0')}:${String(secs).padStart(2, '0')}`
}

const formatPercent = (value = 0) => {
  const safe = Number.isFinite(value) ? Math.max(0, Math.min(100, Math.round(value))) : 0
  return `${safe}%`
}

const labelFromStatus = (status = '') => {
  switch (status.toLowerCase()) {
    case 'downloading':
      return 'Downloading'
    case 'seeding':
      return 'Seeding'
    case 'download_wait':
    case 'check_wait':
      return 'Queued'
    case 'checking':
      return 'Checking'
    case 'seed_wait':
      return 'Finishing'
    case 'stopped':
      return 'Paused'
    default:
      return status || 'Idle'
  }
}

const displayName = (value = '') => value.replace(/\.[^/.]+$/, '')

const fileTitle = (value = '') => {
  const normalized = value.replace(/\\/g, '/')
  const parts = normalized.split('/')
  const base = parts[parts.length - 1] || normalized
  return displayName(base)
}

const getExt = (path = '') => path.toLowerCase().split('.').pop() || ''

const isPlayableVideo = (path = '') => VIDEO_EXTS.includes(getExt(path))
const pathSorter = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

const normalizePath = (value = '') => value.replace(/\\/g, '/')

const getParentPath = (value = '') => {
  const normalized = normalizePath(value)
  const index = normalized.lastIndexOf('/')
  if (index <= 0) return ''
  return normalized.slice(0, index)
}

const encodePath = (value = '') => {
  try {
    const encoded = btoa(encodeURIComponent(value))
    return encoded.replace(/=+$/g, '')
  } catch (err) {
    return ''
  }
}

const decodePath = (value = '') => {
  if (!value) return ''
  const padLength = (4 - (value.length % 4)) % 4
  const padded = value + '='.repeat(padLength)
  try {
    return decodeURIComponent(atob(padded))
  } catch (err) {
    return ''
  }
}

const normalizeTorrentFile = (file) => ({
  index: file?.index ?? file?.Index ?? 0,
  name: file?.name ?? file?.Name ?? '',
  path: file?.path ?? file?.Path ?? '',
  size: file?.size ?? file?.Size ?? 0,
  bytesCompleted: file?.bytesCompleted ?? file?.BytesCompleted ?? 0,
  progress: file?.progress ?? file?.Progress ?? 0,
  streamable: Boolean(file?.streamable ?? file?.Streamable)
})

const normalizeTorrent = (torrent) => {
  const percentDone = torrent?.percentDone ?? torrent?.PercentDone ?? 0
  return {
    id: torrent?.id ?? torrent?.ID ?? 0,
    name: torrent?.name ?? torrent?.Name ?? '',
    status: torrent?.status ?? torrent?.Status ?? '',
    percentDone,
    progress: torrent?.progress ?? torrent?.Progress ?? Math.round(percentDone * 100),
    rateDownload: torrent?.rateDownload ?? torrent?.RateDownload ?? 0,
    eta: torrent?.eta ?? torrent?.ETA ?? 0,
    sizeWhenDone: torrent?.sizeWhenDone ?? torrent?.SizeWhenDone ?? 0,
    downloadedEver: torrent?.downloadedEver ?? torrent?.DownloadedEver ?? 0,
    addedDate: torrent?.addedDate ?? torrent?.AddedDate ?? 0,
    isFinished: torrent?.isFinished ?? torrent?.IsFinished ?? false,
    files: Array.isArray(torrent?.files ?? torrent?.Files)
      ? (torrent.files ?? torrent.Files).map(normalizeTorrentFile)
      : []
  }
}

const buildPlayUrl = (path, follow, nonce) => {
  const params = new URLSearchParams()
  if (follow) params.set('follow', '1')
  if (nonce) params.set('t', nonce)
  const query = params.toString()
  return `/api/play/${encodeURIComponent(path)}${query ? `?${query}` : ''}`
}

const buildVodStartUrl = (path) => `/api/mp4-start/${encodeURIComponent(path)}`
const buildVodStatusUrl = (path) => `/api/mp4-status/${encodeURIComponent(path)}`
const buildVodStreamUrl = (path) => `/api/stream-mp4/${encodeURIComponent(path)}`
const buildDirectUrl = (path) => `/api/stream/${encodeURIComponent(path)}`

const createFolder = (name) => ({
  type: 'folder',
  name,
  children: [],
  map: new Map()
})

const addToTree = (root, parts, payload) => {
  if (parts.length === 1) {
    root.children.push({ type: 'file', name: parts[0], payload })
    return
  }

  const [head, ...rest] = parts
  let child = root.map.get(head)
  if (!child) {
    child = createFolder(head)
    root.map.set(head, child)
    root.children.push(child)
  }
  addToTree(child, rest, payload)
}

const finalizeTree = (node) => {
  node.children.sort((a, b) => {
    if (a.type !== b.type) return a.type === 'folder' ? -1 : 1
    return a.name.localeCompare(b.name)
  })
  node.children.forEach((child) => {
    if (child.type === 'folder') finalizeTree(child)
  })
  delete node.map
}

const buildTree = (items, resolver) => {
  const root = createFolder('root')
  items.forEach((item) => {
    const parts = resolver(item).split('/').filter(Boolean)
    if (parts.length === 0) return
    addToTree(root, parts, item)
  })
  finalizeTree(root)
  return root
}

const buildVideoTree = (videos) => buildTree(videos, (video) => video.path)
const buildTorrentTree = (files) => buildTree(files, (file) => file.path)

function resolveRouteMeta(pathname) {
  if (pathname.startsWith('/library')) return ROUTE_META[1]
  if (pathname.startsWith('/torrents')) return ROUTE_META[2]
  if (pathname.startsWith('/player')) return ROUTE_META[3]
  if (pathname.startsWith('/settings')) return ROUTE_META[4]
  return ROUTE_META[0]
}

function resolveInitialTheme() {
  try {
    const saved = localStorage.getItem('evd-theme')
    if (saved === 'dark' || saved === 'light') return saved
    if (window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches) {
      return 'dark'
    }
  } catch (err) {
    // ignore
  }
  return 'dark'
}

function App() {
  const [theme, setTheme] = useState(resolveInitialTheme)
  const [deviceId] = useState(getOrCreateDeviceId)
  const [watchHistory, setWatchHistory] = useState(() => loadHistoryForDevice(deviceId))
  const [videos, setVideos] = useState([])
  const [activeVideo, setActiveVideo] = useState(null)
  const [playbackUrl, setPlaybackUrl] = useState('')
  const [playbackKind, setPlaybackKind] = useState('idle')
  const [playerState, setPlayerState] = useState('idle')
  const [playerError, setPlayerError] = useState('')
  const [vodState, setVodState] = useState({ status: 'idle', url: '', progress: 0 })

  const [loading, setLoading] = useState(false)
  const [libraryLoading, setLibraryLoading] = useState(true)
  const [torrentLoading, setTorrentLoading] = useState(true)

  const [uploading, setUploading] = useState(false)
  const [uploadProgress, setUploadProgress] = useState(0)
  const [uploadMessage, setUploadMessage] = useState('')
  const [torrentUploading, setTorrentUploading] = useState(false)
  const [torrentMessage, setTorrentMessage] = useState('')

  const [torrents, setTorrents] = useState([])
  const [torrentEnabled, setTorrentEnabled] = useState(true)
  const [torrentError, setTorrentError] = useState('')

  const [toast, setToast] = useState(null)

  const vodPollRef = useRef(null)
  const torrentPollRef = useRef(null)
  const activePathRef = useRef(null)

  const videoInputRef = useRef(null)
  const torrentInputRef = useRef(null)

  const pushToast = useCallback((message, tone = 'info') => {
    setToast({ id: `${Date.now()}-${Math.random()}`, message, tone })
  }, [])

  const dismissToast = useCallback(() => {
    setToast(null)
  }, [])

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
    try {
      localStorage.setItem('evd-theme', theme)
    } catch (err) {
      // ignore
    }
  }, [theme])

  const toggleTheme = useCallback(() => {
    setTheme((value) => (value === 'dark' ? 'light' : 'dark'))
  }, [])

  const watchHistoryByPath = useMemo(() => {
    return new Map(
      watchHistory.map((entry) => [normalizePath(entry.path), entry])
    )
  }, [watchHistory])

  const updateWatchHistory = useCallback((entry) => {
    if (!entry?.path) return
    const nextEntry = {
      ...entry,
      path: normalizePath(entry.path),
      title: entry.title || displayName(entry.path),
      lastWatchedAt: Number.isFinite(entry.lastWatchedAt) ? entry.lastWatchedAt : Date.now()
    }
    setWatchHistory((prev) => mergeHistoryEntry(prev, nextEntry))
  }, [])

  useEffect(() => {
    saveHistoryForDevice(deviceId, watchHistory)
  }, [deviceId, watchHistory])

  useEffect(() => {
    if (!toast) return undefined
    const timeout = setTimeout(() => setToast(null), 3800)
    return () => clearTimeout(timeout)
  }, [toast])

  const fetchVideos = useCallback(async ({ silent = false } = {}) => {
    if (!silent) setLibraryLoading(true)
    try {
      const res = await fetch('/api/videos')
      const data = await res.json()
      setVideos(Array.isArray(data) ? data : [])
    } catch (err) {
      if (!silent) pushToast('Unable to load video library.', 'error')
      setVideos([])
    } finally {
      if (!silent) setLibraryLoading(false)
    }
  }, [pushToast])

  const fetchTorrents = useCallback(async ({ silent = false } = {}) => {
    if (!silent) setTorrentLoading(true)
    try {
      const res = await fetch('/api/torrents')
      const data = await res.json()
      setTorrentEnabled(Boolean(data?.enabled ?? data?.Enabled))
      setTorrentError(data?.error ?? data?.Error ?? '')
      const items = Array.isArray(data?.items ?? data?.Items) ? (data.items ?? data.Items) : []
      setTorrents(items.map(normalizeTorrent))
    } catch (err) {
      setTorrentError('Transmission unavailable')
      setTorrents([])
      if (!silent) pushToast('Unable to reach Transmission backend.', 'error')
    } finally {
      if (!silent) setTorrentLoading(false)
    }
  }, [pushToast])

  useEffect(() => {
    void Promise.all([fetchVideos(), fetchTorrents()])
  }, [fetchVideos, fetchTorrents])

  useEffect(() => {
    let cancelled = false

    const tick = async () => {
      if (cancelled) return
      await Promise.all([fetchTorrents({ silent: true }), fetchVideos({ silent: true })])
      if (!cancelled) {
        torrentPollRef.current = setTimeout(tick, 2600)
      }
    }

    tick()

    return () => {
      cancelled = true
      if (torrentPollRef.current) clearTimeout(torrentPollRef.current)
    }
  }, [fetchTorrents, fetchVideos])

  const stopVodPolling = useCallback(() => {
    if (vodPollRef.current) {
      clearTimeout(vodPollRef.current)
      vodPollRef.current = null
    }
  }, [])

  const pollVodStatus = useCallback(async (path) => {
    if (activePathRef.current !== path) return

    try {
      const res = await fetch(buildVodStatusUrl(path))
      const data = await res.json()

      if (data.ready && data.url) {
        setVodState({ status: 'ready', url: data.url, progress: 100 })
        setPlaybackUrl(buildVodStreamUrl(path))
        setPlaybackKind('vod')
        setPlayerState('buffering')
        stopVodPolling()
        return
      }

      if (data.state === 'failed') {
        setVodState({ status: 'failed', url: '', progress: data.progress ?? 0 })
        setPlayerState('error')
        setPlayerError('MP4 conversion failed.')
        pushToast('MP4 conversion failed for this file.', 'error')
        stopVodPolling()
        return
      }

      setVodState({ status: 'preparing', url: '', progress: data.progress ?? 0 })
    } catch (err) {
      setVodState({ status: 'preparing', url: '', progress: 0 })
    }

    vodPollRef.current = setTimeout(() => {
      void pollVodStatus(path)
    }, 1200)
  }, [pushToast, stopVodPolling])

  const startVod = useCallback(async (path) => {
    stopVodPolling()
    setVodState({ status: 'preparing', url: '', progress: 0 })

    try {
      const res = await fetch(buildVodStartUrl(path), { method: 'POST' })
      if (res.ok) {
        const data = await res.json()
        if (activePathRef.current !== path) return
        if (data.status === 'ready' && data.url) {
          setVodState({ status: 'ready', url: data.url, progress: 100 })
          setPlaybackUrl(buildVodStreamUrl(path))
          setPlaybackKind('vod')
          setPlayerState('buffering')
          return
        }
      }
    } catch (err) {
      setVodState({ status: 'preparing', url: '', progress: 0 })
    }

    if (activePathRef.current !== path) return
    vodPollRef.current = setTimeout(() => {
      void pollVodStatus(path)
    }, 1200)
  }, [pollVodStatus, stopVodPolling])

  const findTorrentFile = useCallback((path) => {
    if (!path) return null
    for (const torrent of torrents) {
      const files = Array.isArray(torrent.files) ? torrent.files : []
      const match = files.find((file) => file.path === path)
      if (match) return match
    }
    return null
  }, [torrents])

  const playVideo = useCallback(async (video, options = {}) => {
    if (!video?.path) return

    const torrentFile = options.torrentFile || findTorrentFile(video.path)
    const follow = Boolean(torrentFile && torrentFile.progress < 100)
    const ext = getExt(video.path)

    setLoading(true)
    setActiveVideo(video)
    activePathRef.current = video.path
    stopVodPolling()
    setVodState({ status: 'idle', url: '', progress: 0 })
    setPlayerError('')
    setPlayerState('connecting')

    if (follow) {
      setPlaybackKind('live')
      setPlaybackUrl(buildPlayUrl(video.path, true, Date.now().toString()))
      setLoading(false)
      return
    }

    if (ext === 'mp4') {
      setPlaybackKind('direct')
      setPlaybackUrl(buildDirectUrl(video.path))
      setLoading(false)
      return
    }

    setPlaybackUrl('')
    setPlaybackKind('vod')
    await startVod(video.path)
    setLoading(false)
  }, [findTorrentFile, startVod, stopVodPolling])

  const uploadVideo = useCallback(async (file) => {
    const extension = file.name.split('.').pop()?.toLowerCase()
    if (!VIDEO_EXTS.includes(extension || '')) {
      setUploadMessage(`Unsupported format. Use ${VIDEO_EXTS.join(', ').toUpperCase()}.`)
      pushToast('Unsupported video format.', 'error')
      return
    }

    const chunkSize = 10 * 1024 * 1024
    const totalChunks = Math.ceil(file.size / chunkSize)

    setUploading(true)
    setUploadProgress(0)
    setUploadMessage('Uploading...')

    try {
      for (let chunkIndex = 0; chunkIndex < totalChunks; chunkIndex += 1) {
        const start = chunkIndex * chunkSize
        const end = Math.min(start + chunkSize, file.size)
        const chunk = file.slice(start, end)

        const formData = new FormData()
        formData.append('chunk', chunk)
        formData.append('fileName', file.name)
        formData.append('chunkIndex', String(chunkIndex))
        formData.append('totalChunks', String(totalChunks))

        const res = await fetch('/api/upload', { method: 'POST', body: formData })
        if (!res.ok) throw new Error('Upload failed')

        await res.json()
        setUploadProgress(Math.round(((chunkIndex + 1) / totalChunks) * 100))
      }

      setUploadMessage('Upload complete.')
      pushToast('Video uploaded successfully.', 'success')
      await fetchVideos({ silent: true })
    } catch (err) {
      setUploadMessage('Upload error. Please try again.')
      pushToast('Video upload failed.', 'error')
    } finally {
      setUploading(false)
      setUploadProgress(0)
    }
  }, [fetchVideos, pushToast])

  const uploadTorrent = useCallback(async (file) => {
    if (!file || !file.name.toLowerCase().endsWith('.torrent')) {
      setTorrentMessage('Choose a .torrent file.')
      pushToast('Select a .torrent file.', 'error')
      return
    }

    setTorrentUploading(true)
    setTorrentMessage('Adding torrent...')

    try {
      const formData = new FormData()
      formData.append('torrent', file)

      const res = await fetch('/api/torrent/upload', {
        method: 'POST',
        body: formData
      })

      if (!res.ok) throw new Error('Upload failed')

      await res.json()
      setTorrentMessage('Torrent added. Download started.')
      pushToast('Torrent added successfully.', 'success')
      await fetchTorrents({ silent: true })
    } catch (err) {
      setTorrentMessage('Torrent upload failed.')
      pushToast('Torrent upload failed.', 'error')
    } finally {
      setTorrentUploading(false)
    }
  }, [fetchTorrents, pushToast])

  const enableTorrentStreaming = useCallback(async (torrentId) => {
    if (!torrentId) return
    try {
      await fetch(`/api/torrent/stream/${torrentId}`, { method: 'POST' })
    } catch (err) {
      pushToast('Failed to switch torrent into stream mode.', 'error')
    }
  }, [pushToast])

  const handleVideoSelect = useCallback((event) => {
    const file = event.target.files?.[0]
    if (file) {
      void uploadVideo(file)
    }
    event.target.value = ''
  }, [uploadVideo])

  const handleTorrentSelect = useCallback((event) => {
    const file = event.target.files?.[0]
    if (file) {
      void uploadTorrent(file)
    }
    event.target.value = ''
  }, [uploadTorrent])

  const activeTorrentFile = activeVideo ? findTorrentFile(activeVideo.path) : null
  const isActiveDownloading = Boolean(activeTorrentFile && activeTorrentFile.progress < 100)
  const videoTree = useMemo(() => buildVideoTree(videos), [videos])

  const contextValue = useMemo(
    () => ({
      videos,
      videoTree,
      torrents,
      torrentEnabled,
      torrentError,
      activeVideo,
      playbackUrl,
      playbackKind,
      playerState,
      playerError,
      vodState,
      loading,
      uploading,
      uploadProgress,
      uploadMessage,
      torrentUploading,
      torrentMessage,
      activeTorrentFile,
      isActiveDownloading,
      libraryLoading,
      torrentLoading,
      deviceId,
      watchHistory,
      watchHistoryByPath,
      videoInputRef,
      torrentInputRef,
      updateWatchHistory,
      setPlayerError,
      setPlayerState,
      playVideo,
      enableTorrentStreaming,
      handleVideoSelect,
      handleTorrentSelect,
      setPlaybackUrl,
      toast,
      dismissToast,
      theme,
      toggleTheme
    }),
    [
      videos,
      videoTree,
      torrents,
      torrentEnabled,
      torrentError,
      activeVideo,
      playbackUrl,
      playbackKind,
      playerState,
      playerError,
      vodState,
      loading,
      uploading,
      uploadProgress,
      uploadMessage,
      torrentUploading,
      torrentMessage,
      activeTorrentFile,
      isActiveDownloading,
      libraryLoading,
      torrentLoading,
      deviceId,
      watchHistory,
      watchHistoryByPath,
      updateWatchHistory,
      playVideo,
      enableTorrentStreaming,
      handleVideoSelect,
      handleTorrentSelect,
      toast,
      dismissToast,
      theme,
      toggleTheme
    ]
  )

  return (
    <Routes>
      <Route element={<Layout contextValue={contextValue} />}>
        <Route index element={<OverviewPage />} />
        <Route path="library" element={<LibraryLayout />}>
          <Route index element={<LibraryIndex />} />
          <Route path=":id" element={<LibraryDetail />} />
        </Route>
        <Route path="torrents" element={<TorrentsPage />} />
        <Route path="player" element={<PlayerPage />} />
        <Route path="settings" element={<SettingsPage />} />
      </Route>
    </Routes>
  )
}

function Layout({ contextValue }) {
  const { activeVideo, torrentEnabled, torrentError, toast, dismissToast, theme, toggleTheme } = contextValue
  const location = useLocation()
  const routeMeta = resolveRouteMeta(location.pathname)

  return (
    <div className="app-shell">
      <aside className="sidebar" aria-label="Main navigation">
        <div className="brand-block">
          <div className="brand-logo">
            <img src={brandImage} alt="EVD logo" />
          </div>
          <div className="brand-copy">
            <strong>Edge Video Deck</strong>
          </div>
        </div>

        <nav className="nav-stack">
          {ROUTE_META.map((item) => (
            <NavLink
              key={item.id}
              to={item.to}
              end={item.to === '/'}
              className={({ isActive }) => cx('nav-link', isActive && 'is-active')}
            >
              <span>{item.label}</span>
            </NavLink>
          ))}
        </nav>

        <div className="sidebar-footer">
          <Badge tone={torrentEnabled && !torrentError ? 'success' : 'danger'}>
            {torrentEnabled && !torrentError ? 'Transmission online' : 'Transmission offline'}
          </Badge>
          <div className="sidebar-now">
            <strong className="text-break">{activeVideo ? displayName(activeVideo.path) : 'No active stream'}</strong>
          </div>
        </div>
      </aside>

      <main className="main-shell">
        <header className="page-header">
          <div>
            <h1>{routeMeta.title}</h1>
          </div>
          <div className="header-meta">
            <Button type="button" variant="ghost" size="sm" onClick={toggleTheme}>
              {theme === 'dark' ? 'Light theme' : 'Dark theme'}
            </Button>
          </div>
        </header>

        <section className="page-content">
          <Outlet context={contextValue} />
        </section>
      </main>

      <ToastStack toast={toast} onDismiss={dismissToast} />
    </div>
  )
}

function OverviewPage() {
  const {
    videos,
    torrents,
    activeVideo,
    isActiveDownloading,
    activeTorrentFile,
    libraryLoading,
    torrentLoading,
    watchHistory,
    playVideo
  } = useOutletContext()
  const navigate = useNavigate()

  const totalSize = useMemo(() => videos.reduce((acc, video) => acc + (video.size || 0), 0), [videos])
  const activeTorrentCount = useMemo(() => torrents.filter((torrent) => !torrent.isFinished).length, [torrents])
  const downloadRate = useMemo(() => torrents.reduce((acc, torrent) => acc + (torrent.rateDownload || 0), 0), [torrents])
  const latestVideo = videos[0]
  const recentHistory = useMemo(() => watchHistory.slice(0, 8), [watchHistory])
  const videoMap = useMemo(
    () => new Map(videos.map((video) => [normalizePath(video.path), video])),
    [videos]
  )

  const handleResume = useCallback(async (path) => {
    const target = videoMap.get(normalizePath(path))
    if (!target) return
    await playVideo(target)
    navigate('/player')
  }, [navigate, playVideo, videoMap])

  return (
    <div className="stack-lg">
      <SectionCard className="hero-card" title="Session">
        <div className="hero-layout">
          <div className="hero-title-block">
            <h2 className="text-break">{activeVideo ? displayName(activeVideo.path) : 'No active stream'}</h2>
            <p>
              {activeVideo
                ? isActiveDownloading && activeTorrentFile
                  ? `Torrent ${formatPercent(activeTorrentFile.progress)}`
                  : 'Ready'
                : 'Idle'}
            </p>
          </div>
          <div className="stats-grid">
            <StatTile label="Videos" value={videos.length} />
            <StatTile label="Library size" value={formatBytes(totalSize)} />
            <StatTile label="Active torrents" value={activeTorrentCount} />
            <StatTile label="Download rate" value={`${formatBytes(downloadRate)}/s`} />
          </div>
        </div>
      </SectionCard>

      <div className="grid-two">
        <SectionCard title="Latest">
          {libraryLoading ? (
            <SkeletonList rows={2} />
          ) : latestVideo ? (
            <div className="inline-item">
              <div className="text-break">
                <strong>{displayName(latestVideo.path)}</strong>
                <p>{formatBytes(latestVideo.size)} · {formatDate(latestVideo.modifiedAt)}</p>
              </div>
              <NavLink className="inline-link" to={`/library/${encodePath(latestVideo.path)}`}>
                Open
              </NavLink>
            </div>
          ) : (
            <EmptyState title="Library is empty" description="Upload a video to get started." compact />
          )}
        </SectionCard>

        <SectionCard title="Torrents">
          {torrentLoading ? (
            <SkeletonList rows={2} />
          ) : (
            <ul className="detail-list">
              <li>Active: {torrents.length}</li>
              <li>Speed: {formatBytes(downloadRate)}/s</li>
            </ul>
          )}
        </SectionCard>
      </div>

      <SectionCard title="Watch history" subtitle="Continue from saved position">
        {recentHistory.length === 0 ? (
          <EmptyState title="No history yet" description="Start playing any video to save progress." compact />
        ) : (
          <div className="stack-md">
            {recentHistory.map((entry) => {
              const available = videoMap.has(normalizePath(entry.path))
              return (
                <div key={entry.path} className="inline-item">
                  <div className="text-break">
                    <strong>{entry.title || displayName(entry.path)}</strong>
                    <p>{formatTime(entry.currentTime)} / {formatTime(entry.duration)} · {formatDateTime(entry.lastWatchedAt)}</p>
                  </div>
                  <div className="toolbar-actions">
                    <NavLink className="inline-link" to={`/library/${encodePath(entry.path)}`}>
                      Details
                    </NavLink>
                    <Button type="button" size="sm" onClick={() => handleResume(entry.path)} disabled={!available}>
                      {available ? `Continue ${formatTime(entry.currentTime)}` : 'Unavailable'}
                    </Button>
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </SectionCard>
    </div>
  )
}

function LibraryLayout() {
  const context = useOutletContext()
  const navigate = useNavigate()
  const { id } = useParams()
  const {
    videos,
    videoTree,
    activeVideo,
    loading,
    libraryLoading,
    videoInputRef,
    handleVideoSelect,
    uploading,
    uploadProgress,
    uploadMessage
  } = context

  const selectedPath = id ? decodePath(id) : ''
  const highlightPath = selectedPath || activeVideo?.path || ''

  useEffect(() => {
    if (!selectedPath) return undefined

    const onKeyDown = (event) => {
      if (event.key === 'Escape') {
        navigate('/library')
      }
    }

    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [navigate, selectedPath])

  return (
    <div className="stack-lg">
      <SectionCard
        title="Library"
        actions={(
          <div className="toolbar-actions">
            <input
              ref={videoInputRef}
              type="file"
              accept="video/*"
              onChange={handleVideoSelect}
              hidden
              aria-hidden="true"
            />
            <Button
              type="button"
              variant="primary"
              onClick={() => videoInputRef.current?.click()}
              disabled={uploading}
              aria-label="Upload video file"
            >
              {uploading ? `Uploading ${formatPercent(uploadProgress)}` : 'Import video'}
            </Button>
          </div>
        )}
      >
        <div className="stack-md">
          {uploading && <ProgressBar value={uploadProgress} />}
          {uploadMessage && <p className="helper-note text-break">{uploadMessage}</p>}

          {libraryLoading ? (
            <SkeletonList rows={6} />
          ) : videos.length === 0 ? (
            <EmptyState title="No videos" description="Import files to start." />
          ) : (
            <div className="tree-shell" aria-busy={loading ? 'true' : 'false'}>
              <FolderTree
                node={videoTree}
                activePath={highlightPath}
                renderFile={(video, isActive) => (
                  <NavLink
                    to={`/library/${encodePath(video.path)}`}
                    className={cx('tree-file', isActive && 'is-active')}
                    aria-current={isActive ? 'true' : undefined}
                  >
                    <div className="tree-file-main text-break">
                      <strong>{displayName(video.path)}</strong>
                      <span>{formatBytes(video.size)} · {formatDate(video.modifiedAt)}</span>
                    </div>
                  </NavLink>
                )}
              />
            </div>
          )}
        </div>
      </SectionCard>

      {selectedPath ? (
        <div
          className="overlay"
          role="dialog"
          aria-modal="true"
          aria-label="Video details"
          onMouseDown={(event) => {
            if (event.target === event.currentTarget) {
              navigate('/library')
            }
          }}
        >
          <div className="overlay-panel">
            <Outlet context={context} />
          </div>
        </div>
      ) : null}
    </div>
  )
}

function FolderTree({ node, renderFile, activePath, depth = 0 }) {
  if (!node) return null

  return node.children.map((child) => {
    const key = `${child.type}-${child.name}-${child.payload?.path || ''}`

    if (child.type === 'folder') {
      return (
        <details key={key} className="tree-folder" open={depth < 2}>
          <summary className="tree-summary">
            <span className="tree-summary-name text-break">{child.name}</span>
            <Badge tone="neutral">{child.children.length}</Badge>
          </summary>
          <div className="tree-children">
            <FolderTree node={child} renderFile={renderFile} activePath={activePath} depth={depth + 1} />
          </div>
        </details>
      )
    }

    const file = child.payload
    return <div key={key}>{renderFile(file, activePath === file.path)}</div>
  })
}

function LibraryIndex() {
  return (
    <EmptyState
      title="Select a file"
      description="Choose an item in the tree."
    />
  )
}

function LibraryDetail() {
  const { id } = useParams()
  const navigate = useNavigate()
  const { videos, playVideo, activeVideo, watchHistoryByPath } = useOutletContext()

  const decoded = decodePath(id)
  const video = videos.find((item) => item.path === decoded)
  const historyEntry = video ? watchHistoryByPath.get(normalizePath(video.path)) : null

  if (!video) {
    return (
      <div className="stack-md">
        <div className="overlay-head">
          <h3>Video not found</h3>
          <Button type="button" variant="ghost" size="sm" onClick={() => navigate('/library')}>
            Close
          </Button>
        </div>
        <EmptyState title="No metadata" description="The file may have been removed or renamed." compact />
      </div>
    )
  }

  const handlePlay = async () => {
    await playVideo(video)
    navigate('/player')
  }

  return (
    <div className="stack-lg">
      <div className="overlay-head">
        <div className="text-break">
          <h3>{displayName(video.path)}</h3>
        </div>
        <div className="toolbar-actions">
          <Button type="button" variant="ghost" size="sm" onClick={() => navigate('/library')}>
            Close
          </Button>
          <Button type="button" variant="primary" size="sm" onClick={handlePlay}>
            {historyEntry?.currentTime > 0
              ? `Continue ${formatTime(historyEntry.currentTime)}`
              : activeVideo?.path === video.path
                ? 'Continue'
                : 'Play'}
          </Button>
        </div>
      </div>

      <div className="detail-grid">
        <div className="detail-cell">
          <span>File</span>
          <strong className="text-break">{video.path}</strong>
        </div>
        <div className="detail-cell">
          <span>Size</span>
          <strong>{formatBytes(video.size)}</strong>
        </div>
        <div className="detail-cell">
          <span>Last modified</span>
          <strong>{formatDate(video.modifiedAt)}</strong>
        </div>
        <div className="detail-cell">
          <span>Progress</span>
          <strong>
            {historyEntry
              ? `${formatTime(historyEntry.currentTime)} / ${formatTime(historyEntry.duration)}`
              : 'Not started'}
          </strong>
        </div>
        <div className="detail-cell">
          <span>Last watched</span>
          <strong>{historyEntry ? formatDateTime(historyEntry.lastWatchedAt) : '—'}</strong>
        </div>
      </div>
    </div>
  )
}

function TorrentsPage() {
  const {
    torrents,
    torrentEnabled,
    torrentError,
    torrentLoading,
    torrentInputRef,
    handleTorrentSelect,
    torrentUploading,
    torrentMessage,
    playVideo,
    enableTorrentStreaming
  } = useOutletContext()

  const navigate = useNavigate()

  const handlePlay = useCallback((torrent, file) => {
    if (!file?.path) return

    const startPlayback = async () => {
      await playVideo({ name: file.name, path: file.path, size: file.size }, { torrentFile: file })
      navigate('/player')
    }

    if (file.progress < 100) {
      enableTorrentStreaming(torrent.id).finally(startPlayback)
    } else {
      void startPlayback()
    }
  }, [enableTorrentStreaming, navigate, playVideo])

  return (
    <SectionCard
      className="torrent-shell"
      title="Torrents"
      actions={(
        <div className="toolbar-actions">
          <input
            ref={torrentInputRef}
            type="file"
            accept=".torrent"
            onChange={handleTorrentSelect}
            hidden
            aria-hidden="true"
          />
          <Button
            type="button"
            variant="primary"
            onClick={() => torrentInputRef.current?.click()}
            disabled={torrentUploading || !torrentEnabled || Boolean(torrentError)}
            aria-label="Upload torrent file"
          >
            {torrentUploading ? 'Importing torrent' : 'Import torrent'}
          </Button>
        </div>
      )}
    >
      <div className="stack-md">
        {torrentMessage && <p className="helper-note text-break">{torrentMessage}</p>}

        {!torrentEnabled && <p className="helper-note">Transmission is not configured.</p>}
        {torrentEnabled && torrentError && <p className="helper-note text-break">{torrentError}</p>}

        {torrentLoading ? (
          <SkeletonList rows={5} />
        ) : torrents.length === 0 ? (
          <EmptyState title="No active torrents" description="Import a .torrent file to start downloading." />
        ) : (
          <div className="torrent-grid">
            {torrents.map((torrent) => (
              <article key={torrent.id} className="torrent-card">
                <div className="torrent-head">
                  <div className="text-break">
                    <h4>{displayName(torrent.name)}</h4>
                    <p>{labelFromStatus(torrent.status)}</p>
                  </div>
                  <Badge tone="accent">{formatPercent(torrent.progress)}</Badge>
                </div>

                <ProgressBar value={torrent.progress} />

                <div className="torrent-meta">
                  <span>{formatBytes(torrent.downloadedEver)} / {formatBytes(torrent.sizeWhenDone)}</span>
                  <span>{formatBytes(torrent.rateDownload)}/s</span>
                  <span>ETA {formatEta(torrent.eta)}</span>
                </div>

                {torrent.files.length > 0 ? (
                  <TorrentTree files={torrent.files} onPlay={(file) => handlePlay(torrent, file)} />
                ) : (
                  <p className="helper-note">Waiting for file list...</p>
                )}
              </article>
            ))}
          </div>
        )}
      </div>
    </SectionCard>
  )
}

function TorrentTree({ files, onPlay }) {
  const tree = useMemo(() => buildTorrentTree(files), [files])

  return (
    <div className="tree-shell compact">
      <FolderTree
        node={tree}
        renderFile={(file) => {
          const canPlay = file.streamable || (isPlayableVideo(file.path || file.name) && file.bytesCompleted > 0)

          return (
            <div className="tree-file">
              <div className="tree-file-main text-break">
                <strong>{fileTitle(file.name)}</strong>
                <span>{formatPercent(file.progress)} · {formatBytes(file.bytesCompleted)} / {formatBytes(file.size)}</span>
              </div>
              <Button
                type="button"
                size="sm"
                onClick={() => onPlay(file)}
                disabled={!canPlay}
                aria-label={canPlay ? 'Play file' : 'File not streamable yet'}
              >
                {file.progress < 100 ? 'Watch now' : 'Play'}
              </Button>
            </div>
          )
        }}
      />
    </div>
  )
}

function PlayerPage() {
  const {
    videos,
    activeVideo,
    loading,
    playbackUrl,
    playbackKind,
    playerState,
    playerError,
    setPlayerError,
    setPlayerState,
    playVideo,
    isActiveDownloading,
    activeTorrentFile,
    setPlaybackUrl,
    vodState,
    watchHistoryByPath,
    updateWatchHistory
  } = useOutletContext()

  const [retrySeed, setRetrySeed] = useState(0)
  const [duration, setDuration] = useState(0)
  const [currentTime, setCurrentTime] = useState(0)
  const videoRef = useRef(null)
  const lastHistorySaveRef = useRef(0)
  const resumePositionRef = useRef(0)

  const historyEntry = useMemo(() => {
    if (!activeVideo?.path) return null
    return watchHistoryByPath.get(normalizePath(activeVideo.path)) || null
  }, [activeVideo?.path, watchHistoryByPath])

  useEffect(() => {
    if (!activeVideo?.path) {
      resumePositionRef.current = 0
      return
    }

    const entry = watchHistoryByPath.get(normalizePath(activeVideo.path))
    resumePositionRef.current = Number.isFinite(entry?.currentTime) ? entry.currentTime : 0
  }, [activeVideo?.path])

  const persistWatchProgress = useCallback((video, { force = false } = {}) => {
    if (!activeVideo?.path || !video) return
    const now = Date.now()

    if (!force && now - lastHistorySaveRef.current < HISTORY_FLUSH_INTERVAL_MS) {
      return
    }

    const nextCurrentTime = Number.isFinite(video.currentTime) && video.currentTime >= 0 ? video.currentTime : 0
    const nextDuration = Number.isFinite(video.duration) && video.duration > 0 ? video.duration : 0

    updateWatchHistory({
      path: activeVideo.path,
      title: displayName(activeVideo.path),
      currentTime: nextCurrentTime,
      duration: nextDuration,
      lastWatchedAt: now
    })

    lastHistorySaveRef.current = now
  }, [activeVideo?.path, updateWatchHistory])

  const handleRetry = useCallback(() => {
    if (!activeVideo?.path) return

    setPlayerState('connecting')
    setRetrySeed((value) => value + 1)
    const nonce = Date.now().toString()

    if (playbackKind === 'live') {
      setPlaybackUrl(buildPlayUrl(activeVideo.path, true, nonce))
      return
    }

    if (playbackKind === 'direct') {
      setPlaybackUrl(`${buildDirectUrl(activeVideo.path)}?t=${nonce}`)
      return
    }

    if (playbackKind === 'vod' && playbackUrl) {
      setPlaybackUrl(`${buildVodStreamUrl(activeVideo.path)}?t=${nonce}`)
    }
  }, [activeVideo?.path, playbackKind, playbackUrl, setPlaybackUrl, setPlayerState])

  useEffect(() => {
    if (!playbackUrl) return undefined
    const video = videoRef.current
    if (!video) return undefined
    const resumeTime = resumePositionRef.current

    setPlayerError('')
    setPlayerState('connecting')

    const onWaiting = () => setPlayerState('buffering')
    const onCanPlay = () => setPlayerState('ready')
    const onPlaying = () => setPlayerState('playing')
    const onEnded = () => {
      setPlayerState('idle')
      persistWatchProgress(video, { force: true })
    }
    const onDuration = () => {
      const nextDuration = Number.isFinite(video.duration) ? video.duration : 0
      setDuration(nextDuration > 0 ? nextDuration : 0)
    }
    const onTimeUpdate = () => {
      setCurrentTime(video.currentTime || 0)
      persistWatchProgress(video)
    }
    const onPause = () => persistWatchProgress(video, { force: true })
    const onLoadedMetadata = () => {
      onDuration()

      const nextDuration = Number.isFinite(video.duration) ? video.duration : 0
      const seekMax = nextDuration > RESUME_GUARD_SECONDS ? nextDuration - RESUME_GUARD_SECONDS : nextDuration
      const resumeAt = Math.min(resumeTime, seekMax)

      if (resumeAt > RESUME_GUARD_SECONDS) {
        video.currentTime = resumeAt
        setCurrentTime(resumeAt)
      }
    }
    const onError = () => {
      setPlayerState('error')
      setPlayerError('Playback error. Try reconnecting.')
    }
    const onBeforeUnload = () => persistWatchProgress(video, { force: true })

    video.addEventListener('waiting', onWaiting)
    video.addEventListener('stalled', onWaiting)
    video.addEventListener('canplay', onCanPlay)
    video.addEventListener('playing', onPlaying)
    video.addEventListener('ended', onEnded)
    video.addEventListener('pause', onPause)
    video.addEventListener('loadedmetadata', onLoadedMetadata)
    video.addEventListener('durationchange', onDuration)
    video.addEventListener('timeupdate', onTimeUpdate)
    video.addEventListener('error', onError)
    window.addEventListener('beforeunload', onBeforeUnload)

    video.load()
    video.play().catch(() => {})

    return () => {
      persistWatchProgress(video, { force: true })
      video.removeEventListener('waiting', onWaiting)
      video.removeEventListener('stalled', onWaiting)
      video.removeEventListener('canplay', onCanPlay)
      video.removeEventListener('playing', onPlaying)
      video.removeEventListener('ended', onEnded)
      video.removeEventListener('pause', onPause)
      video.removeEventListener('loadedmetadata', onLoadedMetadata)
      video.removeEventListener('durationchange', onDuration)
      video.removeEventListener('timeupdate', onTimeUpdate)
      video.removeEventListener('error', onError)
      window.removeEventListener('beforeunload', onBeforeUnload)
    }
  }, [
    persistWatchProgress,
    playbackUrl,
    retrySeed,
    setPlayerError,
    setPlayerState
  ])

  useEffect(() => {
    setCurrentTime(0)
    setDuration(0)
    lastHistorySaveRef.current = 0
  }, [activeVideo?.path, playbackUrl])

  const modeLabel = useMemo(() => {
    if (playbackKind === 'live') return 'Live torrent stream'
    if (playbackKind === 'direct') return 'Direct file stream'
    if (playbackKind === 'vod') return 'Seekable MP4 stream'
    return 'Idle'
  }, [playbackKind])

  const stateTone =
    playerState === 'playing' || playerState === 'ready'
      ? 'success'
      : playerState === 'error'
        ? 'danger'
        : playerState === 'buffering' || playerState === 'connecting'
          ? 'accent'
          : 'neutral'

  const episodeList = useMemo(() => {
    if (!activeVideo?.path) return []
    const activePath = normalizePath(activeVideo.path)
    const activeFolder = getParentPath(activePath)
    const inFolder = videos
      .filter((video) => isPlayableVideo(video.path) && getParentPath(video.path) === activeFolder)
      .sort((a, b) => pathSorter.compare(normalizePath(a.path), normalizePath(b.path)))

    if (!inFolder.some((video) => normalizePath(video.path) === activePath)) {
      inFolder.push(activeVideo)
      inFolder.sort((a, b) => pathSorter.compare(normalizePath(a.path), normalizePath(b.path)))
    }

    return inFolder
  }, [activeVideo, videos])

  const episodeIndex = useMemo(() => {
    if (!activeVideo?.path) return -1
    return episodeList.findIndex((item) => normalizePath(item.path) === normalizePath(activeVideo.path))
  }, [activeVideo?.path, episodeList])

  const handleEpisodeSelect = useCallback(async (path) => {
    const target = episodeList.find((item) => item.path === path)
    if (!target) return
    await playVideo(target)
  }, [episodeList, playVideo])

  const handleEpisodeStep = useCallback(async (step) => {
    if (episodeIndex < 0) return
    const target = episodeList[episodeIndex + step]
    if (!target) return
    await playVideo(target)
  }, [episodeIndex, episodeList, playVideo])

  const canSeek = Number.isFinite(duration) && duration > 0
  const timelineMax = canSeek ? duration : 0
  const timelineValue = canSeek ? Math.min(currentTime, timelineMax) : 0

  const handleSeekTo = useCallback((nextValue) => {
    const video = videoRef.current
    if (!video || !canSeek) return
    const next = Math.min(Math.max(nextValue, 0), timelineMax)
    video.currentTime = next
    setCurrentTime(next)
  }, [canSeek, timelineMax])

  const handleSeekBy = useCallback((delta) => {
    if (!canSeek) return
    handleSeekTo((videoRef.current?.currentTime || 0) + delta)
  }, [canSeek, handleSeekTo])

  return (
    <div className="player-layout">
      <SectionCard
        className="player-card"
        title={activeVideo ? displayName(activeVideo.path) : 'No active stream'}
        actions={(
          <div className="toolbar-actions">
            <Badge tone="neutral">{modeLabel}</Badge>
            <Badge tone={stateTone}>{playerState}</Badge>
            <Button type="button" size="sm" onClick={handleRetry} disabled={!activeVideo}>
              Reconnect
            </Button>
          </div>
        )}
      >
        <div className="player-controls">
          <div className="player-timeline-head">
            <span>{formatTime(timelineValue)}</span>
            <span>{formatTime(timelineMax)}</span>
          </div>
          <input
            className="player-timeline"
            type="range"
            min={0}
            max={timelineMax || 0}
            step={0.1}
            value={timelineValue}
            onChange={(event) => handleSeekTo(Number(event.target.value))}
            disabled={!canSeek || !activeVideo}
            aria-label="Seek timeline"
          />
          <div className="player-timeline-actions">
            <Button type="button" size="sm" onClick={() => handleSeekBy(-10)} disabled={!canSeek || !activeVideo}>
              -10s
            </Button>
            <Button type="button" size="sm" onClick={() => handleSeekBy(10)} disabled={!canSeek || !activeVideo}>
              +10s
            </Button>
          </div>
        </div>

        <div className="episode-switcher">
          <div className="episode-switcher-head">
            <span>Episode</span>
            <span>{episodeIndex >= 0 ? `${episodeIndex + 1}/${episodeList.length}` : '0/0'}</span>
          </div>
          <div className="episode-switcher-row">
            <Button
              type="button"
              size="sm"
              onClick={() => handleEpisodeStep(-1)}
              disabled={loading || episodeIndex <= 0}
            >
              Prev
            </Button>
            <select
              className="episode-select"
              value={activeVideo?.path || ''}
              onChange={(event) => handleEpisodeSelect(event.target.value)}
              disabled={!activeVideo || episodeList.length === 0 || loading}
              aria-label="Select episode"
            >
              {episodeList.map((episode) => (
                <option key={episode.path} value={episode.path}>
                  {fileTitle(episode.path)}
                </option>
              ))}
            </select>
            <Button
              type="button"
              size="sm"
              onClick={() => handleEpisodeStep(1)}
              disabled={loading || episodeIndex < 0 || episodeIndex >= episodeList.length - 1}
            >
              Next
            </Button>
          </div>
        </div>

        <div className="video-shell">
          {activeVideo ? (
            <video ref={videoRef} controls playsInline preload="metadata" src={playbackUrl} />
          ) : (
            <EmptyState title="Player is idle" description="Open a file from Library or Torrents." compact />
          )}
        </div>

        <div className="status-list">
          {historyEntry?.currentTime > 0 && activeVideo && (
            <div className="status-item">Resume point {formatTime(historyEntry.currentTime)}</div>
          )}
          {!canSeek && activeVideo && (
            <div className="status-item">Seek is unavailable until timeline metadata is ready.</div>
          )}
          {vodState.status === 'preparing' && (
            <div className="status-item">Direct: preparing MP4 stream... {vodState.progress > 0 ? `${formatPercent(vodState.progress)}` : ''}</div>
          )}

          {isActiveDownloading && activeTorrentFile && (
            <div className="status-item">
              Torrent progress {formatPercent(activeTorrentFile.progress)} · {formatBytes(activeTorrentFile.bytesCompleted)} / {formatBytes(activeTorrentFile.size)}
            </div>
          )}

          {playerError && <div className="status-item danger">{playerError}</div>}
        </div>
      </SectionCard>
    </div>
  )
}

function SettingsPage() {
  const { torrentEnabled, torrentError, theme, toggleTheme, deviceId, watchHistory } = useOutletContext()

  return (
    <div className="grid-two">
      <SectionCard title="Theme">
        <div className="toolbar-actions">
          <Button type="button" variant="primary" onClick={toggleTheme}>
            {theme === 'dark' ? 'Switch to light' : 'Switch to dark'}
          </Button>
          <Badge tone="neutral">{theme}</Badge>
        </div>
      </SectionCard>

      <SectionCard title="Transmission">
        <Badge tone={torrentEnabled && !torrentError ? 'success' : 'danger'}>
          {torrentEnabled && !torrentError ? 'Online' : 'Offline'}
        </Badge>
      </SectionCard>

      <SectionCard title="Watch history">
        <ul className="detail-list">
          <li>Device ID: <span className="text-break">{deviceId}</span></li>
          <li>Saved items: {watchHistory.length}</li>
        </ul>
      </SectionCard>
    </div>
  )
}

function SectionCard({ title, subtitle, actions, className, children }) {
  return (
    <section className={cx('card', className)}>
      {(title || subtitle || actions) && (
        <div className="card-head">
          <div className="text-break">
            {title && <h3>{title}</h3>}
            {subtitle && <p>{subtitle}</p>}
          </div>
          {actions ? <div className="card-actions">{actions}</div> : null}
        </div>
      )}
      {children}
    </section>
  )
}

function StatTile({ label, value }) {
  return (
    <div className="stat-tile">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  )
}

function Badge({ children, tone = 'neutral' }) {
  return <span className={cx('pill', `pill--${tone}`)}>{children}</span>
}

function Button({ children, className, variant = 'secondary', size = 'md', ...props }) {
  return (
    <button className={cx('btn', `btn--${variant}`, `btn--${size}`, className)} {...props}>
      {children}
    </button>
  )
}

function ProgressBar({ value = 0 }) {
  const safeValue = Math.max(0, Math.min(100, Number.isFinite(value) ? value : 0))
  return (
    <div className="progress" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(safeValue)}>
      <div className="progress-fill" style={{ width: `${safeValue}%` }} />
    </div>
  )
}

function EmptyState({ title, description, compact = false }) {
  return (
    <div className={cx('empty-state', compact && 'compact')}>
      <strong>{title}</strong>
      <p>{description}</p>
    </div>
  )
}

function SkeletonList({ rows = 3 }) {
  const list = Array.from({ length: rows }, (_, idx) => idx)
  return (
    <div className="skeleton-list" aria-hidden="true">
      {list.map((item) => (
        <div key={item} className="skeleton-row">
          <span className="skeleton-line short" />
          <span className="skeleton-line" />
        </div>
      ))}
    </div>
  )
}

function ToastStack({ toast, onDismiss }) {
  if (!toast) return null
  return (
    <div className="toast-stack" role="status" aria-live="polite">
      <div className={cx('toast', `toast--${toast.tone}`)}>
        <span className="text-break">{toast.message}</span>
        <button type="button" className="toast-close" onClick={onDismiss} aria-label="Dismiss notification">
          Close
        </button>
      </div>
    </div>
  )
}

export default App
