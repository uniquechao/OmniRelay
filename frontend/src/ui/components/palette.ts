export const swatchGroups:Record<string,[string,string][]> = {
  neutrals: [
    ['页面背景', 'canvas'], ['内容容器', 'surface'], ['次级容器', 'surface-muted'],
    ['分隔线', 'border'], ['控件边界', 'control-border'], ['主要文字', 'text'],
    ['次级文字', 'text-secondary'], ['辅助文字', 'text-muted']
  ],
  interaction: [
    ['主按钮', 'primary'], ['主按钮悬停', 'primary-hover'], ['主按钮按下', 'primary-pressed'], ['按钮文字', 'on-primary'],
    ['链接', 'link'], ['键盘焦点', 'focus'], ['选中背景', 'selected-bg'],
    ['选中边框', 'selected-edge'], ['控件悬停', 'hover-bg'], ['控件按下', 'pressed-bg'],
    ['次按钮', 'secondary-fill'], ['次按钮文字', 'secondary-ink'], ['次按钮悬停', 'secondary-hover'], ['次按钮按下', 'secondary-pressed'],
    ['信息操作', 'info-action'], ['成功操作', 'success-action'], ['确认操作', 'warning-action'], ['危险操作', 'danger-action'],
    ['禁用背景', 'disabled-bg'], ['禁用文字', 'disabled-text']
  ],
  'rail-colors': [
    ['侧栏背景', 'rail'], ['侧栏文字', 'rail-text'], ['辅助文字', 'rail-muted'], ['分隔线', 'rail-edge'],
    ['菜单悬停', 'rail-hover'], ['菜单按下', 'rail-pressed'], ['当前菜单', 'rail-active'], ['当前图标与焦点', 'rail-accent']
  ],
  'data-colors': [['系列 1', 'chart-1'], ['系列 2', 'chart-2'], ['系列 3', 'chart-3'], ['系列 4', 'chart-4'], ['系列 5', 'chart-5']],
  'avatar-colors': [
    ['深靛蓝', 'avatar-indigo'], ['深青绿', 'avatar-teal'], ['蓝色', 'avatar-blue'],
    ['琥珀色', 'avatar-amber'], ['梅紫色', 'avatar-plum'], ['头像文字', 'avatar-foreground']
  ]
};

export const states:[string,string,string][] = [
  ['待开始', '○', 'pending'], ['进行中', '↻', 'progress'], ['已完成', '✓', 'success'],
  ['重要', '◆', 'important'], ['有风险', '!', 'warning'], ['已阻塞 / 错误', '×', 'danger'],
  ['Agent 提示', '✦', 'agent']
];
