import { Log } from './Middleware/Log.ts'
import { TrimStrings } from './Middleware/TrimStrings.ts'
import { HTTPMiddleware, Kernel as HTTPKernel } from '../../../framework/http.ts'

/**
 * HTTP Kernel class.
 *
 * @export
 * @class Kernel
 */
export class Kernel extends HTTPKernel {

    /**
     * The application's global HTTP middleware stack.
     *
     * @protected
     * @type {HTTPMiddleware[]}
     * @memberof Kernel
     */
    protected middleware: HTTPMiddleware[] = [
        Log
    ]

    /**
     * The application's route middleware groups.
     *
     * @protected
     * @type {{ [key: string]: HTTPMiddleware[] }}
     * @memberof Kernel
     */
    protected middleware_groups: { [key: string]: HTTPMiddleware[] } = {
        web: [
            TrimStrings
        ]
    }

}
