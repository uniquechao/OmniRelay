(() => {
  const page = document.getElementById('login-page');
  const mark = page.querySelector('.login-orbit');
  const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');
  const review = page.querySelector('.login-motion-review');
  const effectButtons = Array.from(review.querySelectorAll('[data-login-effect]'));
  const effectNotes = {
    fade: '整组文案在原位快速交接，画面保持安静。',
    ambient: '背景光晕随主题换色，文案轻柔衔接。',
    cut: '整组文案同步向左滑出，下一组从右侧滑入。'
  };
  let effect = 'cut';
  const controls = page.querySelector('.login-carousel');
  const buttons = Array.from(controls.querySelectorAll('[data-login-slide]'));
  const rotationDelay = 8000;
  let index = 0;
  let timer;
  const copy = [
    [
      '碎片化信息，汇成项目全貌', 'A clear view of every project.',
      '团队的进展散在 IM、会议纪要和任务里。按项目串起来，汇报前也能快速看清现状。',
      '信息归位', '把 IM 对话、会议纪要和任务归到同一个项目。',
      '汇报有据', '从里程碑和简报掌握进展与关键决策。'
    ],
    [
      '任务交下去，进度看得见', 'Know where execution stands.',
      '谁在跟、什么时候交、卡在哪一步，打开项目就能查到。少一轮追问，多一点时间解决问题。',
      '责任到人', '查看任务负责人、截止时间和完成情况。',
      '卡点可见', '按里程碑定位受阻任务，知道先跟进哪一件。'
    ],
    [
      '在延期前，看见小偏差', 'Spot early signs of delay.',
      '一句「再等等」、一次交付改期，可能就是需要介入的信号。从 IM 和任务更新中及时发现变化。',
      '变更线索', '查看任务改期、状态变化和关联对话。',
      '项目简报', '把值得关注的变化带到下一次项目同步。'
    ]
  ];

  const stacks = [
    [page.querySelector('.login-copy-stack'), ['brand-title', 'brand-english', 'brand-detail'], 0],
    [page.querySelector('.login-feature-stack'), ['feature-one', 'feature-one-detail', 'feature-two', 'feature-two-detail'], 3]
  ];
  const slides = stacks.map(([stack, fields, offset]) => {
    const first = stack.firstElementChild;
    return copy.map((text, slideIndex) => {
      const slide = slideIndex === 0 ? first : first.cloneNode(true);
      fields.forEach((field, i) => {
        const element = slide.querySelector('[data-login-copy="' + field + '"]');
        const value = text[offset + i];
        if (field === 'brand-title') {
          const lines = value.split(/(?<=，)/u).map(line => {
            const span = document.createElement('span');
            span.className = 'login-title-line';
            span.textContent = line;
            return span;
          });
          element.replaceChildren(...lines);
        } else {
          element.textContent = value;
        }
      });
      if (slideIndex !== 0) {
        slide.removeAttribute('data-current');
        slide.setAttribute('aria-hidden', 'true');
        stack.append(slide);
      }
      return slide;
    });
  });

  function play() {
    mark.innerHTML = OmniRelayLoading.brandMark();
  }

  function canRotate() {
    return !page.hidden && !document.hidden && !reducedMotion.matches;
  }

  function schedule() {
    clearTimeout(timer);
    if (canRotate()) {
      timer = setTimeout(nextSlide, rotationDelay);
    }
  }

  function selectSlide(next) {
    if (next !== index) {
      slides.forEach(stack => stack.forEach((slide, i) => {
        slide.toggleAttribute('data-outgoing', i === index);
        slide.toggleAttribute('data-entering', i === next);
        slide.toggleAttribute('data-current', i === next);
        slide.setAttribute('aria-hidden', String(i !== next));
      }));
      index = next;
      page.dataset.loginScene = String(index);
      buttons.forEach((button, i) => button.setAttribute('aria-pressed', String(i === index)));
    }
    schedule();
  }

  function nextSlide() {
    selectSlide((index + 1) % copy.length);
  }

  function syncReview() {
    const reviewing = location.hash === '#/login/motion';
    page.toggleAttribute('data-motion-review', reviewing);
    page.dataset.copyEffect = reviewing ? effect : 'cut';
    page.dataset.loginScene = String(index);
    review.hidden = !reviewing;
  }

  effectButtons.forEach(button => {
    button.addEventListener('click', () => {
      effect = button.dataset.loginEffect;
      page.dataset.copyEffect = effect;
      review.querySelector('.login-motion-note').textContent = effectNotes[effect];
      effectButtons.forEach(option => option.setAttribute('aria-pressed', String(option === button)));
      nextSlide();
    });
  });
  review.querySelector('.login-motion-next').addEventListener('click', nextSlide);
  window.addEventListener('hashchange', syncReview);
  syncReview();

  function syncMotion() {
    page.toggleAttribute('data-ambient-paused', !canRotate());
    schedule();
  }

  buttons.forEach((button, i) => {
    button.addEventListener('click', () => selectSlide(i));
  });
  function syncVisibility() {
    if (page.hidden) mark.replaceChildren();
    else play();
    syncMotion();
  }
  new MutationObserver(syncVisibility).observe(page, { attributes: true, attributeFilter: ['hidden'] });
  document.addEventListener('visibilitychange', syncMotion);
  reducedMotion.addEventListener('change', syncMotion);
  controls.hidden = false;
  syncVisibility();
})();
