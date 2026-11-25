import {useEffect, useId, useMemo, useState} from 'react';
import {brandMark} from '../loading';
import wordmark from '../../../../design/prototype/omni-relay-logo-bicolor.png';

export type BrandSlide = {
  label: string;
  title: string;
  english: string;
  detail: string;
  features: readonly [{title: string; detail: string}, {title: string; detail: string}];
};

export function BrandMotion({slides}: {slides: readonly BrandSlide[]}) {
  const [selection, setSelection] = useState({index: 0, previous: -1, revision: 0});
  const [paused, setPaused] = useState(() => document.hidden || matchMedia('(prefers-reduced-motion: reduce)').matches);
  const mark = useMemo(brandMark, []);
  const label = useId();

  useEffect(() => {
    const reduced = matchMedia('(prefers-reduced-motion: reduce)');
    const sync = () => setPaused(document.hidden || reduced.matches);
    document.addEventListener('visibilitychange', sync);
    reduced.addEventListener('change', sync);
    sync();
    return () => {
      document.removeEventListener('visibilitychange', sync);
      reduced.removeEventListener('change', sync);
    };
  }, []);

  useEffect(() => {
    if (paused || slides.length < 2) return;
    const timeout = setTimeout(() => {
      setSelection(current => ({
        index: (current.index + 1) % slides.length,
        previous: current.index,
        revision: current.revision + 1
      }));
    }, 8000);
    return () => clearTimeout(timeout);
  }, [paused, selection, slides.length]);

  function choose(index: number) {
    setSelection(current => ({
      index,
      previous: current.index === index ? current.previous : current.index,
      revision: current.revision + 1
    }));
  }

  const state = (index: number) => ({
    'data-current': index === selection.index ? '' : undefined,
    'data-entering': selection.previous >= 0 && index === selection.index ? '' : undefined,
    'data-outgoing': index === selection.previous ? '' : undefined,
    'aria-hidden': index !== selection.index
  });

  return <section className="login-brand" aria-label="OmniRelay 产品介绍" data-ambient-paused={paused ? '' : undefined}>
    <div className="login-atmosphere" aria-hidden="true">
      {Array.from({length: 10}, (_, index) => <span className="login-meteor" key={index}/>)}
      <div className="login-stars">
        {Array.from({length: 64}, (_, index) => <span className="login-star" key={index} style={{
          left: `${((index * 73 + 19) % 997) / 9.97}%`,
          top: `${((index * index * 37 + 41) % 991) / 9.91}%`,
          animationDelay: `${-(index * .73) % 10}s`
        }}/>)}
      </div>
    </div>
    <img className="login-wordmark" src={wordmark} width="144" height="48" alt="OmniRelay"/>
    <div className="login-copy-stack" id={label}>
      {slides.map((slide, index) => <div className="login-brand-copy" key={slide.label} {...state(index)}>
        <p className="login-english" data-login-copy="brand-english" lang="en">{slide.english}</p>
        <h2 data-login-copy="brand-title">
          {slide.title.split(/(?<=，)/u).map(line => <span className="login-title-line" key={line}>{line}</span>)}
        </h2>
        <p className="login-brand-description" data-login-copy="brand-detail">{slide.detail}</p>
      </div>)}
    </div>
    <div className="login-visual">
      <div className="login-orbit" aria-hidden="true" dangerouslySetInnerHTML={{__html: mark}}/>
    </div>
    <div className="login-feature-stack">
      {slides.map((slide, index) => <ul className="login-features" aria-label="OmniRelay 产品特性" key={slide.label} {...state(index)}>
        {slide.features.map((feature, position) => <li key={position}>
          <span className="login-feature-icon" aria-hidden="true">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
              {position === 0 ? <>
                <path d="m10 8.5-4 6m8-6 4 6m-10 3h8"/>
                <circle cx="12" cy="6.5" r="2.5"/><circle cx="5.5" cy="17.5" r="2.5"/><circle cx="18.5" cy="17.5" r="2.5"/>
              </> : <path d="M20 11a2 2 0 0 0 1-2V6a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h1v4l4-4h2m-5-5h9m1 3 3 3-3 3m-4-3h7"/>}
            </svg>
          </span>
          <div><strong>{feature.title}</strong><p>{feature.detail}</p></div>
        </li>)}
      </ul>)}
    </div>
    <div className="login-carousel" role="group" aria-label="切换产品介绍" aria-controls={label}>
      {slides.map((slide, index) => <button type="button" key={slide.label} data-login-slide={index}
        aria-pressed={index === selection.index} onClick={() => choose(index)}>{slide.label}</button>)}
    </div>
  </section>;
}
