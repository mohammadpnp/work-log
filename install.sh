#!/bin/sh
set -eu
cd "$(dirname "$0")"
task_bin_dir="$HOME/.local/bin"
task_systemd_dir="$HOME/.config/systemd/user"
mkdir -p "$task_bin_dir" "$task_systemd_dir"
go build -o "$task_bin_dir/worklog" .
task_binary="$task_bin_dir/worklog"
task_unit_binary=$(printf '%s' "$task_binary" | sed 's/\\/\\\\/g; s/"/\\"/g; s/%/%%/g')
cat > "$task_systemd_dir/worklog.service" <<EOF
[Unit]
Description=Worklog local cat dashboard
After=default.target

[Service]
ExecStart="$task_unit_binary"
Restart=on-failure
RestartSec=3
UMask=0077

[Install]
WantedBy=default.target
EOF
systemctl --user daemon-reload
systemctl --user enable --now worklog.service
printf '\nWorklog: http://127.0.0.1:7331\nاز داشبورد، ثبت خودکار را فعال کن و پروژه‌ها را اضافه کن.\n'
