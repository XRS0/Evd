# Video Streamer

Сервис стриминга видео (direct/HLS/seekable MP4) с загрузкой торрент-файлов через Transmission.

## Требования

- Go 1.21+
- Node.js 18+
- FFmpeg
- Docker (для Transmission)

## Запуск

### Docker (backend + frontend + transmission)

```bash
docker compose up --build
```

Фронтенд: http://localhost:3000

### Backend

```bash
cd backend
go mod tidy
go run ./cmd/server
```

Сервер запустится на http://localhost:8080

### Frontend

```bash
cd frontend
npm install
npm run dev
```

Фронтенд запустится на http://localhost:3000

## Использование

1. Положите видео файлы (.mp4, .mkv, .avi, .mov) в папку `backend/videos/` (или `./videos` при запуске через Docker)
2. Откройте http://localhost:3000
3. Кликните на видео для воспроизведения

Видео конвертируется по требованию в HLS/MP4, а seekable MP4 для скачанных файлов также подготавливается в фоне.

## Торренты

- В интерфейсе можно загрузить `.torrent` файл.
- При запуске через Docker, Transmission скачивает файлы в `./videos`, и они появляются в библиотеке.
- Прогресс скачивания отображается в секции Torrents.

Если вы запускаете backend без Docker, укажите адрес Transmission:

```bash
export TRANSMISSION_URL=http://localhost:9091/transmission/rpc
export TRANSMISSION_DOWNLOAD_DIR=/downloads
```

## Архитектура

Бэкенд структурирован по DDD + Clean Architecture:

- `internal/domain` — доменные модели
- `internal/application` — use cases и порты
- `internal/infrastructure` — адаптеры (filesystem/ffmpeg/transmission)
- `internal/transport/http` — HTTP транспорт
- `cmd/server` — composition root

Подробная схема: `backend/ARCHITECTURE.md`.
