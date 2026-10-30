import React from 'react';
import { Easing, interpolate, useCurrentFrame } from 'remotion';

const data = [
  { label: 'JAN', value: 42 },
  { label: 'FEB', value: 58 },
  { label: 'MAR', value: 51 },
  { label: 'APR', value: 76 },
  { label: 'MAY', value: 94 },
];

const ink = '#F4F6FC';
const muted = '#8E9AB3';
const accent = '#62E5C1';

export const FiveBarChart = () => {
  const frame = useCurrentFrame();
  const titleOpacity = interpolate(frame, [0, 18], [0, 1], {
    extrapolateLeft: 'clamp', extrapolateRight: 'clamp', easing: Easing.out(Easing.cubic),
  });

  return (
    <div style={{
      position: 'absolute', inset: 0, overflow: 'hidden',
      background: 'radial-gradient(ellipse at 50% 100%, #192b3c 0%, #101a29 48%, #0b1220 100%)',
      color: ink, fontFamily: 'Inter, Avenir Next, Segoe UI, sans-serif',
    }}>
      <div style={{ position: 'absolute', inset: 0, opacity: 0.16, backgroundImage: 'radial-gradient(#70829b 0.8px, transparent 0.8px)', backgroundSize: '28px 28px' }} />
      <div style={{ position: 'absolute', left: 150, top: 111, width: 1620 }}>
        <div style={{ opacity: titleOpacity, display: 'flex', alignItems: 'center', gap: 18, marginBottom: 22 }}>
          <div style={{ width: 12, height: 12, borderRadius: 99, background: accent, boxShadow: '0 0 24px #62e5c188' }} />
          <div style={{ color: accent, fontSize: 20, fontWeight: 700, letterSpacing: 4 }}>GROWTH REPORT · 2025</div>
        </div>
        <div style={{ opacity: titleOpacity, fontSize: 68, fontWeight: 650, letterSpacing: -2.5, lineHeight: 1.1 }}>Momentum, month by month</div>
        <div style={{ opacity: titleOpacity, marginTop: 18, fontSize: 27, color: muted, letterSpacing: 0.2 }}>New customers added · January to May</div>

        <div style={{ position: 'relative', marginTop: 92, height: 500, paddingLeft: 90, paddingRight: 36 }}>
          {[0, 25, 50, 75, 100].map((tick) => {
            const y = 422 - tick * 3.5;
            return <React.Fragment key={tick}>
              <div style={{ position: 'absolute', left: 0, top: y - 13, color: muted, fontSize: 18, fontVariantNumeric: 'tabular-nums' }}>{tick}</div>
              <div style={{ position: 'absolute', left: 55, right: 0, top: y, height: 1, background: tick === 0 ? '#65748b' : '#70809830' }} />
            </React.Fragment>;
          })}
          <div style={{ position: 'absolute', left: 90, right: 30, top: 0, height: 430, display: 'flex', alignItems: 'flex-end', justifyContent: 'space-around' }}>
            {data.map((item, index) => {
              const progress = interpolate(frame, [12 + index * 7, 52 + index * 7], [0, 1], {
                extrapolateLeft: 'clamp', extrapolateRight: 'clamp', easing: Easing.bezier(0.16, 1, 0.3, 1),
              });
              const barHeight = item.value * 3.5 * progress;
              const value = Math.round(item.value * progress);
              return <div key={item.label} style={{ width: 186, height: '100%', position: 'relative', display: 'flex', flexDirection: 'column', justifyContent: 'flex-end', alignItems: 'center' }}>
                <div style={{ position: 'absolute', bottom: barHeight + 25, fontSize: 25, color: ink, fontWeight: 650, fontVariantNumeric: 'tabular-nums', opacity: progress }}>
                  {value}
                </div>
                <div style={{ width: 146, height: barHeight, borderRadius: '14px 14px 3px 3px', background: index === 4 ? 'linear-gradient(180deg, #90F5DA 0%, #52D8B5 100%)' : 'linear-gradient(180deg, #62E5C1 0%, #3FAF9D 100%)', boxShadow: index === 4 && progress > 0.8 ? '0 0 38px #62e5c133' : '0 0 20px #62e5c118', position: 'relative' }}>
                  <div style={{ position: 'absolute', inset: '1px 1px auto', height: 2, borderRadius: 9, background: '#d8fff2aa' }} />
                </div>
                <div style={{ position: 'absolute', bottom: -51, fontSize: 19, letterSpacing: 3, color: index === 4 ? accent : muted, fontWeight: 650 }}>{item.label}</div>
              </div>;
            })}
          </div>
        </div>
        <div style={{ marginTop: 115, display: 'flex', alignItems: 'center', gap: 12, opacity: titleOpacity }}>
          <div style={{ width: 9, height: 9, borderRadius: 99, background: accent }} />
          <div style={{ color: muted, fontSize: 18, letterSpacing: 0.5 }}>Monthly additions</div>
          <div style={{ marginLeft: 'auto', color: accent, fontSize: 21, fontWeight: 650, letterSpacing: 0.5 }}>+124% since January</div>
        </div>
      </div>
      <div style={{ position: 'absolute', right: 150, top: 114, color: '#7F8BA3', fontSize: 18, letterSpacing: 2 }}>01 — 05</div>
    </div>
  );
};
