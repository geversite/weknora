-- Migration: 000010_conflict_status_width (sqlite / Lite mode)
-- SQLite TEXT has no VARCHAR width constraint; kept to align schema versions
-- with PostgreSQL migration 000089.
SELECT 1;
