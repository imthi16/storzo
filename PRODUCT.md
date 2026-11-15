# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users
People across the AI world who keep images and datasets on infrastructure they run themselves: ML and application engineers who upload, browse and verify objects, and platform/ops staff who watch capacity, health and the audit trail. They open the console to get a task done (find an object, check usage, clear a failed upload, confirm storage is reachable), often alongside terminals and dashboards.

## Product Purpose
Storzo is a self-hosted S3 image-storage console: a React UI and a FastAPI management plane over an upstream SeaweedFS S3 server. Applications and AI agents talk S3 to the Storzo gateway (a Go proxy that verifies scoped credentials and records every request); only the browser console uses the management API. It is a prototype for evaluating a MinIO replacement, not a new storage engine. Success: an operator can see what is stored, how much, and whether storage is healthy, and manage buckets and objects without leaving the console.

## Positioning
A console that shows real storage state (capacity per bucket, file-type mix, recent and largest objects, audit trail) on the user's own hardware, with no cloud storage dependency. It must not claim production readiness, HA, or S3 parity that is not proven (see docs/FEATURE_MATRIX.md).

## Visual constraint (volunteered by the user, binding)
Data-center / infrastructure theme, dark. The visual system is recorded in DESIGN.md (green accent, as shipped).
