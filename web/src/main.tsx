import React from 'react';
import {createRoot} from 'react-dom/client';
import App from './App';
import './styles.css';
import './admin.css';
import './editor.css';
import './frontend.css';
import './classroom.css';
import './student.css';
import './classroom-theme.css';
import './teacher-schedule.css';

createRoot(document.getElementById('root')!).render(<React.StrictMode><App/></React.StrictMode>);
