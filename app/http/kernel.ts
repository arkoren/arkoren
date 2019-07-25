import TrimStrings from './middleware/trim_strings.ts'
import { HTTPMiddleware } from '../../../framework/http.ts'

/**
 * HTTP Kernel class.
 *
 * @export
 * @class Kernel
 */
export class Kernel {

    /**
     * The application's global HTTP middleware stack.
     *
     * @protected
     * @type {HTTPMiddleware[]}
     * @memberof Kernel
     */
    protected middleware: HTTPMiddleware[] = [
        TrimStrings
    ]

    /**
     * The application's route middleware groups.
     *
     * @protected
     * @type {{ [key: string]: HTTPMiddleware[] }}
     * @memberof Kernel
     */
    protected middleware_groups: { [key: string]: HTTPMiddleware[] } = {
        'web': [
            TrimStrings
        ]
    }

}
