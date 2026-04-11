-- Copyright 2026 Brian Bouterse
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- You may obtain a copy of the License at
--
--     http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing, software
-- distributed under the License is distributed on an "AS IS" BASIS,
-- WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
-- See the License for the specific language governing permissions and
-- limitations under the License.

-- Store task metadata at dispatch time for efficient querying.
-- Previously these were reconstructed via fragile JOINs and prompt parsing.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS task_name TEXT;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS trigger_type TEXT;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS trigger_ref TEXT;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS repo TEXT;
