import type {BrandSlide} from '../../ui/components/BrandMotion';

export const loginSlides: readonly BrandSlide[] = [
  {
    label: '项目全貌',
    title: '碎片化信息，汇成项目全貌',
    english: 'A clear view of every project.',
    detail: '团队的进展散在 IM、会议纪要和任务里。按项目串起来，汇报前也能快速看清现状。',
    features: [
      {title: '信息归位', detail: '把 IM 对话、会议纪要和任务归到同一个项目。'},
      {title: '汇报有据', detail: '从里程碑和简报掌握进展与关键决策。'}
    ]
  },
  {
    label: '执行追踪',
    title: '任务交下去，进度看得见',
    english: 'Know where execution stands.',
    detail: '谁在跟、什么时候交、卡在哪一步，打开项目就能查到。少一轮追问，多一点时间解决问题。',
    features: [
      {title: '责任到人', detail: '查看任务负责人、截止时间和完成情况。'},
      {title: '卡点可见', detail: '按里程碑定位受阻任务，知道先跟进哪一件。'}
    ]
  },
  {
    label: '细微发现',
    title: '在延期前，看见小偏差',
    english: 'Spot early signs of delay.',
    detail: '一句「再等等」、一次交付改期，可能就是需要介入的信号。从 IM 和任务更新中及时发现变化。',
    features: [
      {title: '变更线索', detail: '查看任务改期、状态变化和关联对话。'},
      {title: '项目简报', detail: '把值得关注的变化带到下一次项目同步。'}
    ]
  }
];
