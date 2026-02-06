# Git Flow для EVD

Этот репозиторий ведется по классическому Git Flow.

## Постоянные ветки

- `main`:
  - только стабильные релизы;
  - каждый merge в `main` сопровождается тегом версии `vX.Y.Z`.
- `develop`:
  - интеграционная ветка;
  - все feature-ветки вливаются сначала сюда.

## Временные ветки

- `feature/<scope>-<short-name>`:
  - ответвляется от `develop`;
  - вливается обратно в `develop` через PR.
  - примеры:
    - `feature/player-seekbar`
    - `feature/torrent-catalog-tree`

- `release/vX.Y.Z`:
  - ответвляется от `develop` на этапе стабилизации релиза;
  - сюда идут только bugfix, docs, version bump;
  - после проверки вливается в `main` и `develop`.

- `hotfix/vX.Y.Z`:
  - ответвляется от `main` для срочных фиксов продакшена;
  - после фикса вливается в `main` и `develop`.

## Процесс работы

1. Создать feature-ветку от `develop`.
2. Сделать коммиты по Conventional Commits (`feat:`, `fix:`, `refactor:` и т.д.).
3. Открыть PR в `develop`.
4. Перед merge обязательно:
   - `go test ./...` в `backend`;
   - `npm run build` в `frontend`;
   - smoke-проверка `docker compose up --build`.
5. Для релиза:
   - создать `release/vX.Y.Z` от `develop`;
   - после QA влить в `main`, поставить тег `vX.Y.Z`;
   - влить ту же release-ветку обратно в `develop`.

## Команды (шаблон)

```bash
# feature
git checkout develop
git pull
git checkout -b feature/player-seekbar

# release
git checkout develop
git pull
git checkout -b release/v1.2.0

# hotfix
git checkout main
git pull
git checkout -b hotfix/v1.2.1
```
