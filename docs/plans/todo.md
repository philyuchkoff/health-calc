# TODO

## Высокий приоритет

- [x] Обновить toolchain Go до 1.27.3 (CVE-2026-111943 / GO-2026-6809, `crypto/x509.Certificate.Verify`) — Dockerfile, `.github/workflows/ci.yml`, `go.mod` (`toolchain go1.27.3`)

## Средний приоритет

- [x] Добавить проверку уязвимостей `govulncheck` в CI
- [x] Актуализировать версию Go в документации (README, README-ru, docs, SRE.md)
- [x] Обновить CHANGELOG.md
- [x] Закрыть Dependabot #1 (CVE-2024-24786): `google.golang.org/protobuf` → v1.33.0, `golang.org/x/sys` → v0.44.0
- [x] Починить CI: `golangci-lint-action` v6 → v9, линтер v2.15.0, миграция `.golangci.yml` на формат v2

## Низкий приоритет

- [x] Завести `docs/plans/todo.md`

## История

- 2026-10-22: hotfix/bump-go-cve-2026-111943 — устранение CVE-2026-111943 (Go crypto/x509): переход на Go 1.27.3, добавлен govulncheck в CI.
- 2026-10-22: chore/ci-golangci-lint-v2 — починен CI: golangci-lint v2.15.0, миграция конфига на формат v2.
