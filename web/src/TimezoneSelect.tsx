import type {ChangeEvent} from 'react';

const groups = [
  {label: '亚洲', zones: ['Asia/Shanghai', 'Asia/Hong_Kong', 'Asia/Taipei', 'Asia/Singapore', 'Asia/Tokyo', 'Asia/Seoul', 'Asia/Bangkok', 'Asia/Ho_Chi_Minh', 'Asia/Jakarta', 'Asia/Manila', 'Asia/Kolkata', 'Asia/Kathmandu', 'Asia/Dubai', 'Asia/Riyadh', 'Asia/Jerusalem']},
  {label: '欧洲', zones: ['Europe/London', 'Europe/Dublin', 'Europe/Paris', 'Europe/Berlin', 'Europe/Amsterdam', 'Europe/Rome', 'Europe/Madrid', 'Europe/Warsaw', 'Europe/Helsinki', 'Europe/Istanbul', 'Europe/Moscow']},
  {label: '美洲', zones: ['America/New_York', 'America/Toronto', 'America/Chicago', 'America/Denver', 'America/Los_Angeles', 'America/Vancouver', 'America/Mexico_City', 'America/Sao_Paulo', 'America/Buenos_Aires']},
  {label: '非洲', zones: ['Africa/Cairo', 'Africa/Lagos', 'Africa/Nairobi', 'Africa/Johannesburg']},
  {label: '大洋洲', zones: ['Australia/Perth', 'Australia/Adelaide', 'Australia/Brisbane', 'Australia/Sydney', 'Australia/Melbourne', 'Pacific/Auckland', 'Pacific/Honolulu']},
];

export const isKnownTimezone = (value: string) => groups.some(group => group.zones.includes(value));

type Props = {
  value: string;
  onChange: (value: string) => void;
  required?: boolean;
};

export default function TimezoneSelect({value, onChange, required}: Props) {
  function change(event: ChangeEvent<HTMLSelectElement>) {
    onChange(event.target.value);
  }

  return <select required={required} value={value} onChange={change}>
    {!isKnownTimezone(value) && value && <option value={value} disabled>{value}（当前值不在选项中，请重新选择）</option>}
    {!value && <option value="" disabled>请选择时区</option>}
    {groups.map(group => <optgroup key={group.label} label={group.label}>
      {group.zones.map(zone => <option key={zone} value={zone}>{zone}</option>)}
    </optgroup>)}
  </select>;
}
