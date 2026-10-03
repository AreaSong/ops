#!/bin/zsh
# 双击打开菜单；所有保存仍需在菜单内预览确认。
tool_dir=${0:A:h}
"${tool_dir}/clash-direct" menu
result=$?
if (( result != 0 )); then
  printf '\n程序退出码：%s。按回车关闭。\n' "$result"
  read -r answer
fi
exit "$result"
