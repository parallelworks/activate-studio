#!/bin/bash
# usage: lo_convert.sh LIST OUTDIR ; LIST lines id<TAB>path. One soffice per file, 90 s cap.
# A private LibreOffice profile, so a running LibreOffice is left alone.
P=-env:UserInstallation=file://${XLS_LO_PROFILE:?set XLS_LO_PROFILE to a scratch directory}
while IFS=$'\t' read -r id path; do
  d="$2/$id"; [ -f "$d/out.xlsx" ] && continue
  mkdir -p "$d/in" && cp "$path" "$d/in/in.xls"
  timeout 90 soffice $P --headless --convert-to xlsx --outdir "$d" "$d/in/in.xls" >/dev/null 2>&1
  [ -f "$d/in.xlsx" ] && mv "$d/in.xlsx" "$d/out.xlsx"
  rm -rf "$d/in"
done < "$1"
